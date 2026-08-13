package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/runstore"
	"github.com/allouis/attractor/internal/setup"
)

// ManagerLoop runs a child pipeline inline (spec §4.11, slimmed per
// local-first plan D6). It exists for children unknowable until runtime
// — the router dispatching an item to a work pipeline. A child known at
// parse time uses `type="subgraph"` static expansion instead. The
// earlier supervisor machinery (poll/steer/cooldown/stop-condition) was
// never used by a shipped pipeline and is gone.
type ManagerLoop struct{}

// Execute runs the child pipeline to completion and maps its terminal
// outcome onto this node's.
func (ManagerLoop) Execute(env engine.HandlerEnv) engine.Outcome {
	childDot := nodeOrGraphAttr(env, "stack.child_dotfile")
	if childDot == "" {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: "manager_loop: graph missing stack.child_dotfile"}
	}

	// Resolve a relative child_dotfile. Precedence:
	//  1. an explicit stack.child_workdir wins (legacy caller intent);
	//  2. else the parent pipeline's directory (its BaseDir), so the path
	//     holds regardless of the process cwd — `attractor run
	//     some/dir/parent.dot` referencing `../sibling/child.dot` works from
	//     anywhere — but only when that resolves to an existing file;
	//  3. else leave it relative to the process cwd (legacy default), so the
	//     daemon (which passes the submission cwd as BaseDir) isn't regressed.
	childWorkdirAttr := nodeOrGraphAttr(env, "stack.child_workdir")
	dotPath := childDot
	if !filepath.IsAbs(dotPath) {
		switch {
		case childWorkdirAttr != "":
			dotPath = filepath.Join(childWorkdirAttr, dotPath)
		case env.Graph.BaseDir != "" && fileExists(filepath.Join(env.Graph.BaseDir, dotPath)):
			dotPath = filepath.Join(env.Graph.BaseDir, dotPath)
		}
	}

	childWorkdir := childWorkdirAttr
	if childWorkdir == "" {
		childWorkdir = filepath.Dir(dotPath)
	}
	// The child run writes under a per-invocation subdir of this stage,
	// through its own engine store (which creates the directory). Empty when
	// the parent run has no logs root, so the child runs no-persistence too.
	//
	// Each Execute gets a FRESH child dir. A revisited manager_loop node (a
	// review fix round re-entering the review child) must re-run the child
	// from scratch against the changed context — not resume the prior round's
	// checkpoint.json and replay its cached lens outputs, the bug that made
	// the review loop of run a5ac1389 unable to converge. Rather than delete
	// the previous checkpoint (which would let the re-run clobber the prior
	// round's artifacts in place), rotate to child, child-2, … so every
	// round's review is preserved for forensics. Probing existing dirs keeps
	// this stateless (no attempt counter to thread through revisits) and
	// entirely on the store seam.
	childLogs := ""
	if env.Stage != nil {
		childLogs = env.Stage.Sub(freshChildSubdir(env.Stage)).Root()
	}

	childSrc, err := os.ReadFile(dotPath)
	if err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: fmt.Sprintf("manager_loop: read %s: %v", dotPath, err)}
	}
	prepared, err := setup.Prepare(setup.Options{
		Source:  string(childSrc),
		BaseDir: childWorkdir,
	})
	if err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: fmt.Sprintf("manager_loop: validate child: %v", err)}
	}
	// A reusable child pipeline that declares no acp_command inherits the
	// parent's (node- then graph-level), so it runs under the same run-wide
	// agent command without needing its own attr or a --acp-cmd flag.
	if prepared.Graph.Attrs["acp_command"] == "" {
		if pa := nodeOrGraphAttr(env, "acp_command"); pa != "" {
			prepared.Graph.Attrs["acp_command"] = pa
		}
	}
	// Likewise the working tree: a child that sets no cwd of its own
	// operates on the parent's resolved cwd. Without this, the child's
	// agents fall back to the attractor process cwd and review the wrong
	// tree (dogfood run 2026-08-13: the five review lenses reviewed
	// attractor's own working copy instead of the target repo). goal
	// deliberately does not carry over (the child mirrors its own graph;
	// see childInitialContext).
	if prepared.Graph.Attrs["cwd"] == "" && env.Cwd != "" {
		prepared.Graph.Attrs["cwd"] = env.Cwd
	}

	registry := env.Registry
	if registry == nil {
		registry = engine.NewRegistry()
	}
	// Seed the child's initial context from the live parent context so it
	// interpolates `$context.*` at runtime (router-spec R3, C6) and its
	// run-start `vars=` validation sees the same values (C3) — no
	// declared-vars conversion.
	initialContext, err := childInitialContext(env)
	if err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: fmt.Sprintf("manager_loop: %v", err)}
	}
	childEng := engine.New(engine.Config{Registry: registry, LogsRoot: childLogs, InitialContext: initialContext})
	childStatus := newChildTelemetry(env.Context)

	// Consume the child's event stream (telemetry shadow + forwarding)
	// while the child runs; drain it fully before returning so nothing
	// calls env.Emit after the parent engine closes its channel.
	consumeDone := make(chan struct{})
	go func() {
		consumeChildEvents(childEng.Events(), childStatus, env)
		close(consumeDone)
	}()
	finalOutcome, _ := childEng.Run(prepared)
	<-consumeDone

	childStatus.markDone(finalOutcome)
	if finalOutcome.Status == engine.StatusSuccess || finalOutcome.Status == engine.StatusPartialSuccess {
		return engine.Outcome{
			Status:         engine.StatusSuccess,
			Notes:          "manager_loop: child completed",
			ContextUpdates: childStatus.contextUpdates(),
		}
	}
	// Distinguish a real review verdict from a failure of the review
	// machinery itself. A synth that WROTE a FAIL verdict is a verdict —
	// it belongs downstream, routed to a fix round. A synth whose
	// status.json never arrived (require_status miss) is machinery
	// failure: surfacing it as a review FAIL feeds a harness error to
	// the fix agent as if it were a finding (the a5ac1389 loop). The
	// downstream fix node reads the findings from
	// $context.stack.child.failure_reason, so the honest machinery label
	// is stamped there too, not just on this node's own reason.
	updates := childStatus.contextUpdates()
	if isRequireStatusMiss(finalOutcome.FailureReason) {
		machinery := "review machinery failed (not a verdict): " + finalOutcome.FailureReason
		updates["stack.child.failure_reason"] = machinery
		// Machinery-classed RETRY (loop-guards LG1): the parent engine
		// re-runs this node — a fresh child dir per invocation — then
		// fails the run; it never routes the harness error to a fix round.
		return engine.Outcome{
			Status:         engine.StatusRetry,
			FailureReason:  "manager_loop: " + machinery,
			FailureClass:   engine.FailureClassMachinery,
			ContextUpdates: updates,
		}
	}
	return engine.Outcome{
		Status:         engine.StatusFail,
		FailureReason:  "manager_loop: child failed — " + finalOutcome.FailureReason,
		ContextUpdates: updates,
	}
}

// freshChildSubdir returns the next unused child-run subdir name under the
// stage: "child" on the first invocation, then "child-2", "child-3", … on each
// revisit. A revisit thereby runs the child engine against an empty dir (no
// checkpoint.json to resume from) while every prior round's artifacts stay put.
func freshChildSubdir(stage *runstore.Dir) string {
	name := "child"
	for n := 2; stage.Exists(name); n++ {
		name = fmt.Sprintf("child-%d", n)
	}
	return name
}

// fileExists reports whether path names an existing regular file.
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// childVarPrefix scopes node attrs that override a child var by name:
// `stack.child.var.pr_number="42"` supplies pr_number to the child.
const childVarPrefix = "stack.child.var."

// childInitialContext seeds the child pipeline's initial context from the
// live parent context (router-spec R3). The child interpolates
// `$context.*` at runtime against these values — the same ones the
// router's conditional edges read — doing programmatically what a human
// types as `-var` on the CLI, without a declared-vars conversion. The
// `graph.*` namespace is excluded so the child mirrors its own graph
// rather than inheriting the parent's cwd/goal. A `stack.child.var.<name>`
// node attr overrides a seeded value; its `$context.*` placeholders are
// expanded against the parent context here (the child's single-pass
// interpolation of `$context.<name>` can't resolve a nested placeholder),
// so a caller can seed e.g. diff_cmd="gh pr diff $context.pr_number …".
func childInitialContext(env engine.HandlerEnv) (map[string]string, error) {
	seed, _ := env.Context.Snapshot()
	for k := range seed {
		if strings.HasPrefix(k, "graph.") {
			delete(seed, k)
		}
	}
	for k, v := range env.Node.Attrs {
		if !strings.HasPrefix(k, childVarPrefix) {
			continue
		}
		expanded, err := env.Context.Expand(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", k, err)
		}
		seed[strings.TrimPrefix(k, childVarPrefix)] = expanded
	}
	return seed, nil
}

// nodeOrGraphAttr reads an attribute from the node, falling back to the
// graph-level attribute (spec deviation A): child selection is a node
// attr so multiple manager_loop nodes coexist in one graph, with the
// graph attr as the single-child fallback. Mirrors the cwd/acp_command
// node-then-graph resolution elsewhere in the engine.
func nodeOrGraphAttr(env engine.HandlerEnv, key string) string {
	if v := env.Node.Attrs[key]; v != "" {
		return v
	}
	return env.Graph.Attrs[key]
}

// childTelemetry shadows the child engine's event stream into the
// parent context as `context.stack.child.*` keys. Designed to be a
// lock-free read for the manager loop body.
type childTelemetry struct {
	parent        *engine.Context
	activeStage   atomic.Value // string
	lastStatus    atomic.Value // string
	recentTools   atomic.Value // string (comma-separated)
	status        atomic.Value // string: running/completed/failed
	failureReason atomic.Value // string
}

func newChildTelemetry(parent *engine.Context) *childTelemetry {
	c := &childTelemetry{parent: parent}
	c.status.Store("starting")
	return c
}

func (c *childTelemetry) markDone(out engine.Outcome) {
	if out.Status == engine.StatusSuccess || out.Status == engine.StatusPartialSuccess {
		c.status.Store("completed")
	} else {
		c.status.Store("failed")
		c.failureReason.Store(out.FailureReason)
	}
	c.observe(c.parent)
}

func (c *childTelemetry) observe(parent *engine.Context) {
	parent.Set("stack.child.status", c.loadStr(&c.status, "running"))
	parent.Set("stack.child.active_stage", c.loadStr(&c.activeStage, ""))
	parent.Set("stack.child.last_status", c.loadStr(&c.lastStatus, ""))
	parent.Set("stack.child.recent_tools", c.loadStr(&c.recentTools, ""))
	parent.Set("stack.child.failure_reason", c.loadStr(&c.failureReason, ""))
}

func (c *childTelemetry) contextUpdates() map[string]string {
	return map[string]string{
		"stack.child.status":         c.loadStr(&c.status, ""),
		"stack.child.active_stage":   c.loadStr(&c.activeStage, ""),
		"stack.child.last_status":    c.loadStr(&c.lastStatus, ""),
		"stack.child.recent_tools":   c.loadStr(&c.recentTools, ""),
		"stack.child.failure_reason": c.loadStr(&c.failureReason, ""),
	}
}

func (c *childTelemetry) loadStr(v *atomic.Value, def string) string {
	raw := v.Load()
	if raw == nil {
		return def
	}
	return raw.(string)
}

// consumeChildEvents pumps the child engine's event stream into the
// telemetry shadow. Runs in its own goroutine for the duration of the
// child run.
func consumeChildEvents(stream <-chan engine.Event, t *childTelemetry, env engine.HandlerEnv) {
	tools := []string{}
	for ev := range stream {
		switch ev.Kind {
		case engine.EventStageStarted:
			t.activeStage.Store(ev.NodeID)
			t.status.Store("running")
		case engine.EventStageCompleted, engine.EventStageFailed:
			t.lastStatus.Store(ev.Status)
		case engine.EventStageProgress:
			if ev.Detail != nil {
				if tool := ev.Detail["tool"]; tool != "" {
					tools = append(tools, tool)
					if len(tools) > 5 {
						tools = tools[len(tools)-5:]
					}
					t.recentTools.Store(strings.Join(tools, ","))
				}
			}
		}
		if env.Emit != nil {
			ev.Detail = mergeDetail(ev.Detail, "source", "child")
			env.Emit(ev)
		}
	}
}

func mergeDetail(d map[string]string, k, v string) map[string]string {
	if d == nil {
		d = map[string]string{}
	}
	d[k] = v
	return d
}
