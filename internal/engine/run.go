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
	"time"

	"github.com/fabro/attractor/internal/artifact"
	"github.com/fabro/attractor/internal/graph"
	"github.com/fabro/attractor/internal/lint"
	"github.com/fabro/attractor/internal/transform"
)

// Engine runs Attractor pipelines. A single Engine instance executes one
// run at a time; the events channel is closed when the run terminates.
type Engine struct {
	Registry        *Registry
	LogsRoot        string
	RunID           string
	Artifacts       *artifact.Store
	MaxLoopRestarts int
	events          chan Event
	rng             *mrand.Rand
	now             func() time.Time
	restartCount    int
}

// Config configures a new Engine.
type Config struct {
	Registry  *Registry
	LogsRoot  string
	RunID     string
	EventsBuf int
	Now       func() time.Time
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
		RunID:           runID,
		Artifacts:       artifact.New(filepath.Join(cfg.LogsRoot, "artifacts")),
		MaxLoopRestarts: 100,
		events:          make(chan Event, buf),
		rng:             mrand.New(mrand.NewSource(time.Now().UnixNano())),
		now:             cfg.Now,
	}
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
// preprocessing (e.g. PromptFile, parametric VariableExpansion) is
// authoritative — the built-in pass then handles the remainder.
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
	if err := os.MkdirAll(e.LogsRoot, 0o755); err != nil {
		return failOutcome(fmt.Sprintf("create logs root: %v", err)), err
	}

	state, err := e.loadOrInitState(g)
	if err != nil {
		return failOutcome(err.Error()), err
	}

	e.emit(Event{Kind: EventPipelineStarted, Timestamp: e.now(), RunID: e.RunID, NodeID: state.cursor})

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

		outcome, err := e.executeNodeWithRetry(g, node, state)
		if err != nil {
			return e.fail(err.Error())
		}

		// Record completion and persist artifacts. Only SUCCESS-class
		// outcomes go in completedNodes so a resumed run will re-execute
		// a failed stage from scratch.
		state.context.Apply(outcome.ContextUpdates)
		state.context.Set("outcome", outcome.Status.String())
		state.context.Set("preferred_label", outcome.PreferredLabel)
		if outcome.Status == StatusSuccess || outcome.Status == StatusPartialSuccess {
			state.completedNodes = append(state.completedNodes, nodeID)
			state.nodeOutcomes[nodeID] = outcome
			if err := e.writeStatus(nodeID, outcome); err != nil {
				return e.fail(fmt.Sprintf("write status.json: %v", err))
			}
			if err := e.saveCheckpoint(state); err != nil {
				return e.fail(fmt.Sprintf("save checkpoint: %v", err))
			}
			e.emit(Event{Kind: EventCheckpointSaved, Timestamp: e.now(), RunID: e.RunID, NodeID: nodeID})
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
			state = e.resetState(g, next)
			continue
		}
		state.previousNode = nodeID
		state.incomingEdge = advanceEdge
		state.cursor = next
	}
}

// runState bundles the live execution state held across one Run.
type runState struct {
	cursor              string
	completedNodes      []string
	nodeOutcomes        map[string]Outcome
	context             *Context
	retries             map[string]int
	shouldSkipCompleted bool
	// incomingEdge tracks the edge by which the engine arrived at the
	// current cursor; used to resolve per-edge fidelity / thread_id
	// overrides for the next handler invocation.
	incomingEdge *graph.Edge
	previousNode string
}

func (e *Engine) loadOrInitState(g *graph.Graph) (*runState, error) {
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
			shouldSkipCompleted: true,
		}, nil
	}
	ctx := NewContext()
	ctx.MirrorGraph(g)
	if err := e.writeManifest(g); err != nil {
		return nil, err
	}
	return &runState{
		cursor:         findStartNode(g),
		nodeOutcomes:   map[string]Outcome{},
		context:        ctx,
		retries:        map[string]int{},
	}, nil
}

func (e *Engine) resetState(g *graph.Graph, startAt string) *runState {
	e.archiveCurrentLogs()
	ctx := NewContext()
	ctx.MirrorGraph(g)
	return &runState{
		cursor:       startAt,
		nodeOutcomes: map[string]Outcome{},
		context:      ctx,
		retries:      map[string]int{},
	}
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
	archive := filepath.Join(e.LogsRoot, fmt.Sprintf("_restart_%d", e.restartCount))
	if err := os.MkdirAll(archive, 0o755); err != nil {
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
		Goal:           g.Goal(),
		RunID:          e.RunID,
		CompletedNodes: state.completedNodes,
		NodeOutcomes:   state.nodeOutcomes,
		Context:        ctxValues,
		Responses:      e.readRecentResponses(state.completedNodes, 5),
	})
	env := HandlerEnv{
		Node:      node,
		Graph:     g,
		Context:   state.context,
		LogsRoot:  e.LogsRoot,
		RunID:     e.RunID,
		Emit:      e.emit,
		Registry:  e.Registry,
		Artifacts: e.Artifacts,
		Fidelity:  fidelity,
		ThreadID:  threadID,
		Preamble:  preamble,
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
			outcome.FailureReason = "max retries exceeded"
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

func (e *Engine) writeManifest(g *graph.Graph) error {
	m := Manifest{
		RunID:     e.RunID,
		GraphName: g.Name,
		Goal:      g.Goal(),
		StartedAt: e.now(),
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(e.LogsRoot, "manifest.json"), data, 0o644)
}

func (e *Engine) writeStatus(nodeID string, outcome Outcome) error {
	dir := filepath.Join(e.LogsRoot, nodeID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	outcome.Finalize()
	data, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "status.json"), data, 0o644)
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
	return os.WriteFile(filepath.Join(e.LogsRoot, "checkpoint.json"), data, 0o644)
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

func (e *Engine) emit(ev Event) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = e.now()
	}
	if ev.RunID == "" {
		ev.RunID = e.RunID
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

func findEdge(g *graph.Graph, from, to string) *graph.Edge {
	for _, e := range g.OutgoingEdges(from) {
		if e.To == to {
			return e
		}
	}
	return nil
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
