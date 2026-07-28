package handler

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/allouis/attractor/internal/condition"
	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	graphpkg "github.com/allouis/attractor/internal/graph"
)

// ManagerLoop supervises a child pipeline (spec §4.11). The child .dot
// is given by the graph-level `stack.child_dotfile` attribute; the
// supervisor cycles through observe/steer/wait actions until the child
// terminates or a stop condition fires.
//
// Steering is currently best-effort: the manager records intent in the
// parent context (`context.stack.steering`) so a Codergen backend that
// supports mid-flight steering can consume it. Without such a backend
// the steer action is logged as a no-op.
type ManagerLoop struct{}

// Execute runs the supervisor cycle.
func (ManagerLoop) Execute(env engine.HandlerEnv) engine.Outcome {
	childDot := nodeOrGraphAttr(env, "stack.child_dotfile")
	if childDot == "" {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: "manager_loop: graph missing stack.child_dotfile"}
	}
	pollInterval := 45 * time.Second
	if d, ok := env.Node.Duration("manager.poll_interval"); ok {
		pollInterval = d
	}
	maxCycles := env.Node.Int("manager.max_cycles", 1000)
	stopCondition := env.Node.Attrs["manager.stop_condition"]
	actions := parseActions(env.Node.Attrs["manager.actions"])

	autostart := true
	if v, ok := env.Node.Attrs["stack.child_autostart"]; ok && v != "true" {
		autostart = false
	}

	cooldown := 120 * time.Second
	if d, ok := env.Node.Duration("manager.steer_cooldown"); ok {
		cooldown = d
	}

	childWorkdir := nodeOrGraphAttr(env, "stack.child_workdir")
	if childWorkdir == "" {
		childWorkdir = filepath.Dir(childDot)
	}
	childLogs := filepath.Join(env.LogsRoot, env.Node.ID, "child")
	if err := os.MkdirAll(childLogs, 0o755); err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: fmt.Sprintf("manager_loop: %v", err)}
	}

	dotPath := childDot
	if !filepath.IsAbs(dotPath) {
		dotPath = filepath.Join(childWorkdir, dotPath)
	}
	childSrc, err := os.ReadFile(dotPath)
	if err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: fmt.Sprintf("manager_loop: read %s: %v", dotPath, err)}
	}
	file, err := dot.Parse(string(childSrc))
	if err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: fmt.Sprintf("manager_loop: parse child: %v", err)}
	}
	childGraph, err := graphpkg.Build(file)
	if err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: fmt.Sprintf("manager_loop: build child: %v", err)}
	}
	prepared, err := engine.Prepare(childGraph)
	if err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: fmt.Sprintf("manager_loop: validate child: %v", err)}
	}

	registry := env.Registry
	if registry == nil {
		registry = engine.NewRegistry()
	}
	childEng := engine.New(engine.Config{Registry: registry, LogsRoot: childLogs})
	childStatus := newChildTelemetry(env.Context)
	doneCh := make(chan engine.Outcome, 1)

	if autostart {
		go func() {
			consumeDone := make(chan struct{})
			go func() {
				consumeChildEvents(childEng.Events(), childStatus, env)
				close(consumeDone)
			}()
			out, _ := childEng.Run(prepared)
			// Wait for the event consumer to drain before reporting done, so
			// it never calls env.Emit after Execute returns and the parent
			// engine closes its event channel (send-on-closed-channel race).
			<-consumeDone
			doneCh <- out
		}()
	}

	steerLast := time.Time{}
	for cycle := 1; cycle <= maxCycles; cycle++ {
		select {
		case finalOutcome := <-doneCh:
			childStatus.markDone(finalOutcome)
			if finalOutcome.Status == engine.StatusSuccess || finalOutcome.Status == engine.StatusPartialSuccess {
				return engine.Outcome{
					Status:         engine.StatusSuccess,
					Notes:          "manager_loop: child completed",
					ContextUpdates: childStatus.contextUpdates(),
				}
			}
			return engine.Outcome{
				Status:         engine.StatusFail,
				FailureReason:  "manager_loop: child failed — " + finalOutcome.FailureReason,
				ContextUpdates: childStatus.contextUpdates(),
			}
		default:
		}

		if actions["observe"] {
			childStatus.observe(env.Context)
		}
		if actions["steer"] && time.Since(steerLast) >= cooldown {
			steered := steerChild(env)
			if steered {
				steerLast = time.Now()
			}
		}
		if stopCondition != "" {
			expr, err := condition.Parse(stopCondition)
			if err == nil {
				if expr.Evaluate(engine.Resolver{Context: env.Context}) {
					return engine.Outcome{
						Status:         engine.StatusSuccess,
						Notes:          "manager_loop: stop condition satisfied",
						ContextUpdates: childStatus.contextUpdates(),
					}
				}
			}
		}
		if actions["wait"] {
			time.Sleep(pollInterval)
		}
	}
	return engine.Outcome{
		Status:        engine.StatusFail,
		FailureReason: "manager_loop: max cycles exceeded",
	}
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

// parseActions converts the comma-separated `manager.actions` attribute
// into a lookup set. Empty or absent defaults to observe + wait.
func parseActions(raw string) map[string]bool {
	out := map[string]bool{}
	if raw == "" {
		out["observe"] = true
		out["wait"] = true
		return out
	}
	for _, part := range strings.Split(raw, ",") {
		out[strings.TrimSpace(part)] = true
	}
	return out
}

// steerChild reads any queued steering message from
// `context.stack.steering.next` and pushes it down to the child via
// `context.stack.child.steering`. The codergen backend layer is
// expected to surface this to the agent; for backends without
// mid-flight steering (current MVP) this is a no-op breadcrumb.
func steerChild(env engine.HandlerEnv) bool {
	msg := env.Context.Get("stack.steering.next")
	if msg == "" {
		return false
	}
	env.Context.Set("stack.child.steering", msg)
	env.Context.Set("stack.steering.next", "")
	return true
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
