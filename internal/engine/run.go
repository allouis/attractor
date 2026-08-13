package engine

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	mrand "math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/lint"
	"github.com/allouis/attractor/internal/runstore"
	"github.com/allouis/attractor/internal/transform"
)

// Engine runs Attractor pipelines. A single Engine instance executes one
// run at a time; the events channel is closed when the run terminates.
type Engine struct {
	Registry        *Registry
	LogsRoot        string
	store           *runstore.Dir
	RunID           string
	MaxLoopRestarts int
	events          chan Event
	eventsFile      *os.File
	eventsMu        sync.Mutex
	rng             *mrand.Rand
	now             func() time.Time
	restartCount    int
	usageMu         sync.Mutex
	usageTotal      Usage
	// visitsMu/visits mirror the run state's per-node visit counts for
	// emit, which stamps Event.Visit (D3 span identity) and cannot see
	// the run state directly (handlers call emit concurrently).
	visitsMu       sync.Mutex
	visits         map[string]int
	seq            atomic.Int64
	initialContext map[string]string
	skipEventLog   bool
}

// Config configures a new Engine.
type Config struct {
	Registry  *Registry
	LogsRoot  string
	RunID     string
	EventsBuf int
	Now       func() time.Time
	// InitialContext seeds the run's context at start (applied after
	// MirrorGraph, before the first node). Lets the submit path feed Item
	// vars + `item.*` metadata to the run's conditional edges
	// (router-spec deviation B). Nil for runs with no seed.
	InitialContext map[string]string
	// SkipEventLog disables the engine's own events.jsonl persistence. Set by a
	// reporting child whose logs root is SHARED with the daemon (the vm rw-9p
	// transport, ui-run-view-v3 P5c): the daemon is then the single writer of
	// events.jsonl (built from the events the child forwards over phone-home),
	// keeping every path in the shared run dir single-writer. The child still
	// streams every event home; only the local duplicate is suppressed.
	SkipEventLog bool
}

// New constructs an Engine with the supplied config.
func New(cfg Config) *Engine {
	buf := cfg.EventsBuf
	if buf <= 0 {
		buf = 64
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	runID := cfg.RunID
	if runID == "" {
		runID = newRunID()
	}
	return &Engine{
		Registry:        cfg.Registry,
		LogsRoot:        cfg.LogsRoot,
		store:           newStore(cfg.LogsRoot),
		RunID:           runID,
		MaxLoopRestarts: 100,
		events:          make(chan Event, buf),
		rng:             mrand.New(mrand.NewSource(time.Now().UnixNano())),
		now:             cfg.Now,
		initialContext:  cfg.InitialContext,
		skipEventLog:    cfg.SkipEventLog,
	}
}

// newStore returns the run-artifact write seam rooted at logsRoot, or nil
// when there is no logs root (a no-persistence run). nil is deliberate: it
// makes the engine skip artifact writes rather than fall back to a raw,
// possibly-relative path that could land in the process cwd.
func newStore(logsRoot string) *runstore.Dir {
	if logsRoot == "" {
		return nil
	}
	return runstore.New(logsRoot)
}

// stageStore returns the write seam for one visit of a node — the
// per-visit stage directory {node_id}/v{visit} (spec amendment A1).
// Every visit gets a fresh dir so a failed loop's history stays
// inspectable; mirrorStage copies the latest visit to the node root for
// spec §5.6 consumers. nil when the run has no logs root.
func (e *Engine) stageStore(nodeID string, visit int) *runstore.Dir {
	if e.store == nil {
		return nil
	}
	if visit < 1 {
		visit = 1
	}
	return e.store.Sub(nodeID).Sub(fmt.Sprintf("v%d", visit))
}

// mirrorStage copies the visit dir's tree to the node root, so the
// node root always reflects the latest visit (spec §5.6). Best-effort:
// a run whose mirror fails still has the authoritative v{N} artifacts.
func (e *Engine) mirrorStage(nodeID string, visit int) {
	if e.store == nil {
		return
	}
	src := filepath.Join(e.LogsRoot, nodeID, fmt.Sprintf("v%d", visit))
	dst := e.store.Sub(nodeID)
	_ = filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		_ = dst.Write(rel, data)
		return nil
	})
}

// Events returns the read-only event channel. The channel is closed
// after Run completes.
func (e *Engine) Events() <-chan Event { return e.events }

// PreparedGraph wraps a graph that has been linted and transform-applied.
// Callers use Prepare to obtain one before Run.
type PreparedGraph struct {
	Graph       *graph.Graph
	Diagnostics []lint.Diagnostic
}

// Prepare parses, transforms, and validates a graph for execution.
// Extra transforms run before the built-in pipeline so caller-supplied
// preprocessing (e.g. PromptFile) is authoritative — the built-in pass
// then handles the remainder.
func Prepare(g *graph.Graph, extra ...transform.Transform) (*PreparedGraph, error) {
	g, err := transform.Apply(g, append(append([]transform.Transform{}, extra...), transform.BuiltIn()...))
	if err != nil {
		return nil, fmt.Errorf("transform: %w", err)
	}
	diags, err := lint.ValidateOrError(g)
	if err != nil {
		return nil, err
	}
	return &PreparedGraph{Graph: g, Diagnostics: diags}, nil
}

// Run executes the prepared graph and returns the pipeline-level outcome.
// The Engine's events channel is closed when Run returns.
func (e *Engine) Run(pg *PreparedGraph) (Outcome, error) {
	defer close(e.events)
	g := pg.Graph
	if e.store != nil {
		if err := e.store.MkdirAll(); err != nil {
			return failOutcome(fmt.Sprintf("create logs root: %v", err)), err
		}
	}
	e.openEventsFile()
	defer e.closeEventsFile()

	state, fresh, err := e.loadOrInitState(g)
	if err != nil {
		return e.fail(err.Error())
	}

	e.emit(Event{Kind: EventPipelineStarted, Timestamp: e.now(), RunID: e.RunID, NodeID: state.cursor})

	// Validate the `vars=` input contract against the seeded context
	// (spec §"Locked decisions" 6): every declared required key must be
	// present before any node runs. Missing inputs fail the run fast;
	// genuinely mid-run keys are caught later at the referencing node.
	// Fresh runs only — a resumed run was already admitted at its start.
	if fresh {
		if missing := missingDeclaredVar(g, state.context); missing != "" {
			return e.fail(fmt.Sprintf("missing required input %q declared in vars=", missing))
		}
	}

	for {
		nodeID := state.cursor
		node, ok := g.Nodes[nodeID]
		if !ok {
			return e.fail("missing node: " + nodeID)
		}
		state.context.Set("current_node", nodeID)

		if isTerminalNode(nodeID, node) {
			if jumpTo, failedGate, ok := e.checkGoalGates(g, state); !ok {
				if jumpTo != "" {
					state.context.Set("internal.goal_gate.failed", failedGate)
					state.cursor = jumpTo
					continue
				}
				return e.fail(fmt.Sprintf("goal gate %q unsatisfied and no retry target", failedGate))
			}
			outcome := Outcome{Status: StatusSuccess, Notes: "Pipeline completed"}
			outcome.Finalize()
			e.emit(Event{Kind: EventPipelineCompleted, Timestamp: e.now(), RunID: e.RunID, Status: outcome.StatusString})
			return outcome, nil
		}

		// Skip if already completed in a previous incarnation of this run.
		if _, completed := state.nodeOutcomes[nodeID]; completed && state.shouldSkipCompleted {
			outcome := state.nodeOutcomes[nodeID]
			next, advanceEdge := e.advanceOrFail(g, node, &outcome, state)
			if next == "" {
				return outcome, nil
			}
			state.previousNode = nodeID
			state.incomingEdge = advanceEdge
			state.cursor = next
			continue
		}

		// Bound revisits so failure loops (retry edges bouncing between
		// nodes) terminate instead of running forever. Node `max_visits`
		// overrides the graph-level `max_node_visits`; absent/zero means
		// unlimited.
		state.visits[nodeID]++
		e.setVisit(nodeID, state.visits[nodeID])
		if limit := node.Int("max_visits", g.IntAttr("max_node_visits", 0)); limit > 0 && state.visits[nodeID] > limit {
			return e.fail(fmt.Sprintf("node %q exceeded max_node_visits (%d)", nodeID, limit))
		}

		outcome, err := e.executeNodeWithRetry(g, node, state)
		if err != nil {
			return e.fail(err.Error())
		}

		// A machinery-classed failure (loop-guards LG1) terminates the
		// run with its own reason: routing it through outcome=fail edges
		// would send a fix agent to "address" a harness error it cannot
		// fix — the a5ac1389 ghost-chasing loop.
		if outcome.Status == StatusFail && outcome.FailureClass == FailureClassMachinery {
			e.emit(Event{Kind: EventPipelineFailed, Timestamp: e.now(), RunID: e.RunID, NodeID: nodeID, Message: outcome.FailureReason})
			return outcome, nil
		}

		// Stuck-loop breaker (loop-guards LG2): the same node failing with
		// the identical reason N consecutive times means the loop is not
		// converging — abort now instead of burning rounds until
		// max_node_visits. Changing reasons are progress and never trip it.
		if msg := state.recordFailure(g, nodeID, outcome); msg != "" {
			return e.fail(msg)
		}

		// Record completion and persist artifacts. Only SUCCESS-class
		// outcomes go in completedNodes so a resumed run will re-execute
		// a failed stage from scratch.
		state.context.Apply(outcome.ContextUpdates)
		state.context.Set("outcome", outcome.Status.String())
		state.context.Set("preferred_label", outcome.PreferredLabel)
		// Mirror the failure reason so a downstream node routed via an
		// outcome=fail edge can read what failed ($context.failure_reason)
		// — inlined review subgraphs route synth FAIL verdicts to fix
		// nodes this way (D6).
		state.context.Set("failure_reason", outcome.FailureReason)
		if outcome.Status == StatusSuccess || outcome.Status == StatusPartialSuccess {
			state.completedNodes = append(state.completedNodes, nodeID)
			state.nodeOutcomes[nodeID] = outcome
			if err := e.writeStatus(nodeID, state.visits[nodeID], outcome); err != nil {
				return e.fail(fmt.Sprintf("write status.json: %v", err))
			}
			// Mirror before the checkpoint event: consumers keying on
			// checkpoint_saved (the phone-home stage uploader) must see
			// the node root already reflecting this visit.
			e.mirrorStage(nodeID, state.visits[nodeID])
			if err := e.saveCheckpoint(state); err != nil {
				return e.fail(fmt.Sprintf("save checkpoint: %v", err))
			}
			e.emit(Event{Kind: EventCheckpointSaved, Timestamp: e.now(), RunID: e.RunID, NodeID: nodeID})
		} else {
			// Failed visits mirror too — the root should show the latest
			// visit whatever its outcome (spec §5.6, amendment A1).
			e.mirrorStage(nodeID, state.visits[nodeID])
		}

		next, advanceEdge := e.advanceOrFail(g, node, &outcome, state)
		if next == "" {
			if outcome.Status == StatusFail {
				e.emit(Event{Kind: EventPipelineFailed, Timestamp: e.now(), RunID: e.RunID, Message: outcome.FailureReason})
				return outcome, nil
			}
			done := Outcome{Status: StatusSuccess, Notes: "Pipeline completed"}
			done.Finalize()
			e.emit(Event{Kind: EventPipelineCompleted, Timestamp: e.now(), RunID: e.RunID, Status: done.StatusString})
			return done, nil
		}
		if advanceEdge != nil && advanceEdge.Bool("loop_restart") {
			if e.restartCount >= e.MaxLoopRestarts {
				return e.fail(fmt.Sprintf("loop_restart cap reached (%d restarts)", e.restartCount))
			}
			e.emit(Event{
				Kind:    EventStageProgress,
				Message: fmt.Sprintf("loop_restart: restarting at %s", next),
			})
			var err error
			state, err = e.resetState(g, next)
			if err != nil {
				return e.fail(err.Error())
			}
			e.resetVisits()
			// resetState archived the prior incarnation's logs (including
			// events.jsonl); reopen a fresh file for the new incarnation.
			e.openEventsFile()
			continue
		}
		state.previousNode = nodeID
		state.incomingEdge = advanceEdge
		state.cursor = next
	}
}

// missingDeclaredVar returns the first `vars=` key absent from ctx, or ""
// when every declared input is present. The engine is the one run-start
// validation site shared by all entry points (CLI, serve, automation,
// cron); each seeds its vars into the initial context before Run.
func missingDeclaredVar(g *graph.Graph, ctx *Context) string {
	for _, name := range g.DeclaredVars() {
		if _, ok := ctx.Lookup(name); !ok {
			return name
		}
	}
	return ""
}

// runState bundles the live execution state held across one Run.
type runState struct {
	cursor              string
	completedNodes      []string
	nodeOutcomes        map[string]Outcome
	context             *Context
	retries             map[string]int
	visits              map[string]int
	shouldSkipCompleted bool
	// incomingEdge tracks the edge by which the engine arrived at the
	// current cursor; used to resolve per-edge fidelity / thread_id
	// overrides for the next handler invocation.
	incomingEdge *graph.Edge
	previousNode string
	// lastFailReason/failStreak back the LG2 stuck-loop breaker: per
	// node, the reason of the last failure and how many consecutive
	// failures repeated it verbatim.
	lastFailReason map[string]string
	failStreak     map[string]int
}

// recordFailure updates the LG2 stuck-loop bookkeeping for a node
// outcome and returns a non-empty abort message when the same node has
// failed with the identical (trimmed) reason max_repeated_failures
// times in a row (graph attr, default 3; 0 disables).
func (s *runState) recordFailure(g *graph.Graph, nodeID string, outcome Outcome) string {
	if s.failStreak == nil {
		s.failStreak = map[string]int{}
		s.lastFailReason = map[string]string{}
	}
	if outcome.Status != StatusFail {
		delete(s.failStreak, nodeID)
		delete(s.lastFailReason, nodeID)
		return ""
	}
	reason := strings.TrimSpace(outcome.FailureReason)
	if s.failStreak[nodeID] > 0 && reason == s.lastFailReason[nodeID] {
		s.failStreak[nodeID]++
	} else {
		s.failStreak[nodeID] = 1
		s.lastFailReason[nodeID] = reason
	}
	limit := g.IntAttr("max_repeated_failures", 3)
	if limit > 0 && s.failStreak[nodeID] >= limit {
		return fmt.Sprintf(
			"stuck loop: node %q failed %d consecutive times with the same reason — no progress is being made. Reason: %s",
			nodeID, s.failStreak[nodeID], reason)
	}
	return ""
}

// loadOrInitState resumes from a checkpoint when one exists, else builds a
// fresh run. fresh reports which: it gates run-start input validation,
// which is a first-start gate — a resumed run was already admitted at its
// original start and must not be re-validated against its (possibly older)
// checkpoint context.
func (e *Engine) loadOrInitState(g *graph.Graph) (state *runState, fresh bool, err error) {
	ckpt, err := e.loadCheckpoint()
	if err == nil && ckpt != nil {
		ctx := NewContext()
		ctx.Apply(ckpt.ContextValues)
		next := e.resumeAfter(g, ckpt)
		if next == "" {
			next = findStartNode(g)
		}
		return &runState{
			cursor:              next,
			completedNodes:      append([]string{}, ckpt.CompletedNodes...),
			nodeOutcomes:        ckpt.NodeOutcomes,
			context:             ctx,
			retries:             ckpt.NodeRetries,
			visits:              map[string]int{},
			shouldSkipCompleted: true,
		}, false, nil
	}
	ctx, err := e.freshContext(g)
	if err != nil {
		return nil, false, err
	}
	if err := e.writeManifest(g, ctx.Get("graph.goal")); err != nil {
		return nil, false, err
	}
	return &runState{
		cursor:       findStartNode(g),
		nodeOutcomes: map[string]Outcome{},
		context:      ctx,
		retries:      map[string]int{},
		visits:       map[string]int{},
	}, true, nil
}

func (e *Engine) resetState(g *graph.Graph, startAt string) (*runState, error) {
	e.archiveCurrentLogs()
	ctx, err := e.freshContext(g)
	if err != nil {
		return nil, err
	}
	return &runState{
		cursor:       startAt,
		nodeOutcomes: map[string]Outcome{},
		context:      ctx,
		retries:      map[string]int{},
		visits:       map[string]int{},
	}, nil
}

// freshContext builds the context for a run starting from scratch (not
// resumed from a checkpoint): graph attrs mirrored into `graph.*`, then
// the seeded initial values applied on top (router-spec deviation B).
// Finally the graph goal is resolved once against the seeded context and
// frozen into `graph.goal` (spec decision 7), so the run summary and every
// node's `$goal` read the resolved text. An undefined key fails fast
// (decision 4) rather than carrying a raw placeholder forward.
func (e *Engine) freshContext(g *graph.Graph) (*Context, error) {
	ctx := NewContext()
	ctx.MirrorGraph(g)
	// Seed the human-gate note key so a prompt referencing $context.human.note
	// resolves on the first visit — before any gate has been answered.
	// wait.human overwrites it with each answer's note (handler.WaitHuman).
	// Seeded before the initial context so an explicit seed can still win.
	ctx.Set("human.note", "")
	ctx.Apply(e.initialContext)
	resolved, err := ctx.Expand(g.Goal())
	if err != nil {
		return nil, err
	}
	ctx.Set("graph.goal", resolved)
	return ctx, nil
}

// archiveCurrentLogs moves the current run's logs subtree aside before
// resetting so prior artefacts remain inspectable. Files that fail to
// move are ignored; the goal is best-effort preservation, not strict
// atomicity.
func (e *Engine) archiveCurrentLogs() {
	if e.LogsRoot == "" {
		return
	}
	e.restartCount++
	archiveName := fmt.Sprintf("_restart_%d", e.restartCount)
	archive := filepath.Join(e.LogsRoot, archiveName)
	if err := e.store.Sub(archiveName).MkdirAll(); err != nil {
		return
	}
	entries, err := os.ReadDir(e.LogsRoot)
	if err != nil {
		return
	}
	for _, ent := range entries {
		name := ent.Name()
		if strings.HasPrefix(name, "_restart_") || name == "artifacts" {
			continue
		}
		_ = os.Rename(filepath.Join(e.LogsRoot, name), filepath.Join(archive, name))
	}
}

func (e *Engine) resumeAfter(g *graph.Graph, ckpt *Checkpoint) string {
	// Find the edge to follow after the last completed node using its
	// recorded outcome.
	if ckpt.CurrentNode == "" {
		return findStartNode(g)
	}
	node, ok := g.Nodes[ckpt.CurrentNode]
	if !ok {
		return ""
	}
	outcome := ckpt.LastOutcome
	if outcome == nil {
		// Pre-MVP fallback: use the saved outcome map.
		if v, ok := ckpt.NodeOutcomes[ckpt.CurrentNode]; ok {
			cp := v
			outcome = &cp
		}
	}
	if edge := SelectEdge(node, outcome, NewContextFrom(ckpt.ContextValues), g); edge != nil {
		return edge.To
	}
	return ""
}

// NewContextFrom builds a Context pre-populated from a values map.
func NewContextFrom(values map[string]string) *Context {
	c := NewContext()
	c.Apply(values)
	return c
}

// executeNodeWithRetry runs the handler with the node's retry policy.
func (e *Engine) executeNodeWithRetry(g *graph.Graph, node *graph.Node, state *runState) (Outcome, error) {
	policy := nodeRetryPolicy(node, g)
	fidelity := ResolveFidelity(state.incomingEdge, node, g)
	threadID := ""
	if fidelity == FidelityFull {
		threadID = ResolveThread(state.incomingEdge, node, g, state.previousNode)
	}
	ctxValues, _ := state.context.Snapshot()
	preamble := BuildPreamble(PreambleInput{
		Mode:           fidelity,
		Goal:           state.context.Get("graph.goal"),
		RunID:          e.RunID,
		CompletedNodes: state.completedNodes,
		NodeOutcomes:   state.nodeOutcomes,
		Context:        ctxValues,
		Responses:      e.readRecentResponses(state.completedNodes, 5),
	})
	cwd := node.Attrs["cwd"]
	if cwd == "" {
		cwd = g.Attrs["cwd"]
	}
	env := HandlerEnv{
		Node:      node,
		Graph:     g,
		Stage:     e.stageStore(node.ID, state.visits[node.ID]),
		Context:   state.context,
		LogsRoot:  e.LogsRoot,
		RunID:     e.RunID,
		Emit:      e.emit,
		Registry:  e.Registry,
		Fidelity:  fidelity,
		ThreadID:  threadID,
		Preamble:  preamble,
		Cwd:       cwd,
	}
	handler, err := e.Registry.Resolve(node)
	if err != nil {
		return Outcome{}, err
	}

	var outcome Outcome
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		e.emit(Event{Kind: EventStageStarted, Timestamp: e.now(), RunID: e.RunID, NodeID: node.ID, Attempt: attempt})
		start := e.now()
		outcome = handler.Execute(env)
		outcome.Finalize()
		dur := e.now().Sub(start)
		switch outcome.Status {
		case StatusSuccess, StatusPartialSuccess, StatusSkipped:
			e.emit(Event{
				Kind: EventStageCompleted, Timestamp: e.now(), RunID: e.RunID,
				NodeID: node.ID, Status: outcome.StatusString, Duration: dur,
			})
			state.retries[node.ID] = 0
			return outcome, nil
		case StatusRetry:
			if attempt < policy.MaxAttempts {
				state.retries[node.ID] = attempt
				delay := policy.Backoff.DelayForAttempt(attempt, e.rng)
				e.emit(Event{
					Kind: EventStageRetrying, Timestamp: e.now(), RunID: e.RunID,
					NodeID: node.ID, Attempt: attempt, DelayMs: int(delay / time.Millisecond),
					Message: outcome.FailureReason,
				})
				time.Sleep(delay)
				continue
			}
			if node.Bool("allow_partial") {
				outcome.Status = StatusPartialSuccess
				outcome.Notes = "retries exhausted, partial accepted"
				outcome.Finalize()
				return outcome, nil
			}
			outcome.Status = StatusFail
			// Keep the underlying reason (and its failure class) — a bare
			// "max retries exceeded" hides what went wrong (LG1).
			if outcome.FailureReason != "" {
				outcome.FailureReason = "max retries exceeded: " + outcome.FailureReason
			} else {
				outcome.FailureReason = "max retries exceeded"
			}
			outcome.Finalize()
			e.emit(Event{
				Kind: EventStageFailed, Timestamp: e.now(), RunID: e.RunID,
				NodeID: node.ID, Status: outcome.StatusString, Message: outcome.FailureReason,
			})
			return outcome, nil
		case StatusFail:
			e.emit(Event{
				Kind: EventStageFailed, Timestamp: e.now(), RunID: e.RunID,
				NodeID: node.ID, Status: outcome.StatusString, Message: outcome.FailureReason,
			})
			return outcome, nil
		}
	}
	return outcome, nil
}

// advanceOrFail returns the next node ID and the edge used to reach
// it (nil if the advance was via NextNode jump or retry_target). Empty
// string signals "no further progress possible" — the caller decides
// between SUCCESS exit and FAIL exit based on the last outcome.
func (e *Engine) advanceOrFail(g *graph.Graph, node *graph.Node, outcome *Outcome, state *runState) (string, *graph.Edge) {
	if outcome.NextNode != "" {
		if _, ok := g.Nodes[outcome.NextNode]; ok {
			return outcome.NextNode, nil
		}
	}
	if edge := SelectEdge(node, outcome, state.context, g); edge != nil {
		return edge.To, edge
	}
	if outcome.Status == StatusFail {
		return firstResolvedTarget(g, node, "retry_target", "fallback_retry_target"), nil
	}
	return "", nil
}

// checkGoalGates returns (jumpTarget, failingNodeID, satisfied). When
// satisfied is false and jumpTarget is empty, no retry route exists and
// the pipeline must fail.
func (e *Engine) checkGoalGates(g *graph.Graph, state *runState) (string, string, bool) {
	for _, id := range state.completedNodes {
		node, ok := g.Nodes[id]
		if !ok {
			continue
		}
		if !node.Bool("goal_gate") {
			continue
		}
		outcome, ok := state.nodeOutcomes[id]
		if !ok {
			continue
		}
		if outcome.Status == StatusSuccess || outcome.Status == StatusPartialSuccess {
			continue
		}
		if t := firstResolvedTarget(g, node, "retry_target", "fallback_retry_target"); t != "" {
			return t, id, false
		}
		return "", id, false
	}
	return "", "", true
}

// firstResolvedTarget returns the first attribute value among keys that
// resolves to an existing node. It tries the node first, then the
// graph-level attribute of the same name. Empty string when nothing
// resolves.
func firstResolvedTarget(g *graph.Graph, node *graph.Node, keys ...string) string {
	for _, k := range keys {
		if t := node.Attrs[k]; t != "" {
			if _, ok := g.Nodes[t]; ok {
				return t
			}
		}
	}
	for _, k := range keys {
		if t := g.Attrs[k]; t != "" {
			if _, ok := g.Nodes[t]; ok {
				return t
			}
		}
	}
	return ""
}

// writeManifest persists the engine's run identity record to run.json. It is
// deliberately NOT manifest.json: on the direct runner the daemon owns
// manifest.json (the fleet record, carrying the run id) in the same logs root,
// and a shared filename made the two writers clobber each other for the whole
// run window (ui-run-view-v3 P5a). Separate filenames give every run-dir path a
// single writer. The JSON field names are unchanged, so old dirs still load.
func (e *Engine) writeManifest(g *graph.Graph, goal string) error {
	m := Manifest{
		RunID:     e.RunID,
		GraphName: g.Name,
		Goal:      goal,
		StartedAt: e.now(),
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if e.store == nil {
		return nil
	}
	return e.store.Write("run.json", data)
}

// writeStatus persists the engine's outcome record into the node's
// visit dir; the mirror pass copies it (with the rest of the visit's
// artifacts) to the node root.
func (e *Engine) writeStatus(nodeID string, visit int, outcome Outcome) error {
	outcome.Finalize()
	data, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return err
	}
	if e.store == nil {
		return nil
	}
	return e.stageStore(nodeID, visit).Write("status.json", data)
}

func (e *Engine) saveCheckpoint(state *runState) error {
	values, logs := state.context.Snapshot()
	var lastOutcome *Outcome
	if n := len(state.completedNodes); n > 0 {
		last := state.completedNodes[n-1]
		if o, ok := state.nodeOutcomes[last]; ok {
			cp := o
			lastOutcome = &cp
		}
	}
	currentNode := ""
	if n := len(state.completedNodes); n > 0 {
		currentNode = state.completedNodes[n-1]
	}
	ckpt := Checkpoint{
		Timestamp:      e.now(),
		CurrentNode:    currentNode,
		CompletedNodes: state.completedNodes,
		NodeRetries:    state.retries,
		ContextValues:  values,
		Logs:           logs,
		LastOutcome:    lastOutcome,
		NodeOutcomes:   state.nodeOutcomes,
	}
	data, err := json.MarshalIndent(ckpt, "", "  ")
	if err != nil {
		return err
	}
	if e.store == nil {
		return nil
	}
	return e.store.Write("checkpoint.json", data)
}

func (e *Engine) loadCheckpoint() (*Checkpoint, error) {
	data, err := os.ReadFile(filepath.Join(e.LogsRoot, "checkpoint.json"))
	if err != nil {
		return nil, err
	}
	var ckpt Checkpoint
	if err := json.Unmarshal(data, &ckpt); err != nil {
		return nil, err
	}
	// Re-attach Status enum from string for resumed outcomes.
	for id, o := range ckpt.NodeOutcomes {
		o.Status = ParseStatus(o.StatusString)
		ckpt.NodeOutcomes[id] = o
	}
	if ckpt.LastOutcome != nil {
		ckpt.LastOutcome.Status = ParseStatus(ckpt.LastOutcome.StatusString)
	}
	return &ckpt, nil
}

// openEventsFile (re)opens events.jsonl in append mode. Persistence
// lives in the engine so both entry points — CLI `run` and the serve
// daemon — record events, making any run replayable (service-spec §3).
// A failure to open is non-fatal: the run proceeds without persistence.
func (e *Engine) openEventsFile() {
	e.closeEventsFile()
	if e.store == nil || e.skipEventLog {
		return
	}
	f, err := e.store.OpenAppend("events.jsonl")
	if err != nil {
		return
	}
	e.eventsFile = f
}

func (e *Engine) closeEventsFile() {
	if e.eventsFile != nil {
		_ = e.eventsFile.Close()
		e.eventsFile = nil
	}
}

// setVisit records the current visit number for a node so emit can
// stamp it on events. resetVisits clears the map on a loop_restart (a
// fresh incarnation restarts visit counting, matching run state).
func (e *Engine) setVisit(nodeID string, visit int) {
	e.visitsMu.Lock()
	defer e.visitsMu.Unlock()
	if e.visits == nil {
		e.visits = map[string]int{}
	}
	e.visits[nodeID] = visit
}

func (e *Engine) resetVisits() {
	e.visitsMu.Lock()
	defer e.visitsMu.Unlock()
	e.visits = nil
}

func (e *Engine) visitOf(nodeID string) int {
	e.visitsMu.Lock()
	defer e.visitsMu.Unlock()
	return e.visits[nodeID]
}

func (e *Engine) emit(ev Event) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = e.now()
	}
	if ev.RunID == "" {
		ev.RunID = e.RunID
	}
	if ev.Visit == 0 && ev.NodeID != "" {
		ev.Visit = e.visitOf(ev.NodeID)
	}
	ev.Seq = e.seq.Add(1)
	// Accumulate per-stage usage and attach the run rollup to the
	// terminal pipeline event (service-spec §6). Guarded because parallel
	// handler branches may emit concurrently.
	switch ev.Kind {
	case EventUsage:
		if ev.Usage != nil {
			e.usageMu.Lock()
			e.usageTotal.Add(*ev.Usage)
			e.usageMu.Unlock()
		}
	case EventPipelineCompleted, EventPipelineFailed:
		if ev.Usage == nil {
			e.usageMu.Lock()
			total := e.usageTotal
			e.usageMu.Unlock()
			if total.InputTokens != 0 || total.OutputTokens != 0 {
				ev.Usage = &total
			}
		}
	}
	// Persist before the channel send so a slow (or full-buffer) consumer
	// never costs us a durable event.
	if e.eventsFile != nil {
		if data, err := json.Marshal(ev); err == nil {
			// One write of the payload+newline under a lock so concurrent
			// emits never interleave a line and its terminator.
			data = append(data, '\n')
			e.eventsMu.Lock()
			_, _ = e.eventsFile.Write(data)
			e.eventsMu.Unlock()
		}
	}
	select {
	case e.events <- ev:
	default:
		// Drop on full buffer rather than block traversal. Consumers
		// concerned about loss should buffer adequately.
	}
}

func (e *Engine) fail(reason string) (Outcome, error) {
	out := failOutcome(reason)
	e.emit(Event{Kind: EventPipelineFailed, Timestamp: e.now(), RunID: e.RunID, Message: reason})
	return out, fmt.Errorf("%s", reason)
}

func failOutcome(reason string) Outcome {
	o := Outcome{Status: StatusFail, FailureReason: reason}
	o.Finalize()
	return o
}

func findStartNode(g *graph.Graph) string {
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		if n.Shape() == "Mdiamond" || id == "start" || id == "Start" {
			return id
		}
	}
	return ""
}

func isTerminalNode(id string, n *graph.Node) bool {
	if n.Shape() == "Msquare" {
		return true
	}
	return id == "exit" || id == "end"
}

func nodeRetryPolicy(node *graph.Node, g *graph.Graph) RetryPolicy {
	def := g.IntAttr("default_max_retries", g.IntAttr("default_max_retry", 0))
	// We can't introspect "explicit vs inherited" cleanly from raw attrs;
	// use the value if present.
	raw, ok := node.Attrs["max_retries"]
	max := def
	if ok && raw != "" {
		max = node.Int("max_retries", def)
	}
	if max <= 0 {
		return PolicyNone()
	}
	p := PolicyStandard()
	p.MaxAttempts = max + 1
	return p
}

// readRecentResponses reads response.md for the most recent N completed
// nodes so BuildPreamble can inline them. Limit caps the read so very
// long runs don't pull entire log subtrees into memory.
func (e *Engine) readRecentResponses(completed []string, limit int) map[string]string {
	if e.LogsRoot == "" || len(completed) == 0 {
		return nil
	}
	start := len(completed) - limit
	if start < 0 {
		start = 0
	}
	out := make(map[string]string, len(completed)-start)
	for _, id := range completed[start:] {
		data, err := os.ReadFile(filepath.Join(e.LogsRoot, id, "response.md"))
		if err != nil {
			continue
		}
		out[id] = string(data)
	}
	return out
}

func newRunID() string { return NewRunID() }

// NewRunID returns a short random run identifier (12 hex chars).
// Exposed so callers can mint IDs without spinning up an Engine.
func NewRunID() string {
	b := make([]byte, 6)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(256))
		b[i] = byte(n.Int64())
	}
	return hex.EncodeToString(b)
}
