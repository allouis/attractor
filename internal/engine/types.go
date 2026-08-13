// Package engine implements Attractor's pipeline execution loop (spec §3).
// It owns graph traversal, edge selection, retry, goal-gate enforcement,
// and event emission; handler implementations live in sibling packages.
package engine

import (
	"strings"
	"sync"
	"time"

	"github.com/allouis/attractor/internal/graph"
)

// Status is the outcome state of a handler execution (spec §5.2).
type Status int

const (
	StatusUnknown Status = iota
	StatusSuccess
	StatusPartialSuccess
	StatusRetry
	StatusFail
	StatusSkipped
)

// String returns the lowercase canonical name used in status.json files
// and condition expressions.
func (s Status) String() string {
	switch s {
	case StatusSuccess:
		return "success"
	case StatusPartialSuccess:
		return "partial_success"
	case StatusRetry:
		return "retry"
	case StatusFail:
		return "fail"
	case StatusSkipped:
		return "skipped"
	}
	return "unknown"
}

// ParseStatus converts a canonical status string back into a Status
// value. Unknown strings yield StatusUnknown.
func ParseStatus(s string) Status {
	// Case-insensitive: agents echo the casing their prompts use ("outcome
	// SUCCESS"), and rejecting "FAIL" for casing cost run 9afacdba seven
	// review rounds whose verdicts were on disk, correct, and unread.
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "success":
		return StatusSuccess
	case "partial_success":
		return StatusPartialSuccess
	case "retry":
		return StatusRetry
	case "fail":
		return StatusFail
	case "skipped":
		return StatusSkipped
	}
	return StatusUnknown
}

// FailureClassMachinery marks a failure of the harness itself (a
// require_status verdict that never arrived, a contract violation that
// survived correction) as opposed to a task failure the agent authored.
// The engine refuses to route machinery failures through outcome=fail
// edges — a fix agent cannot fix the harness (loop-guards LG1).
const FailureClassMachinery = "machinery"

// Outcome is the return value of a handler. Fields mirror Attractor
// Appendix C so an Outcome serializes verbatim into status.json. The
// engine-only NextNode field lets composite handlers (parallel)
// redirect traversal without owning their own edges.
type Outcome struct {
	Status           Status            `json:"-"`
	StatusString     string            `json:"outcome"`
	PreferredLabel   string            `json:"preferred_label,omitempty"`
	SuggestedNextIDs []string          `json:"suggested_next_ids,omitempty"`
	ContextUpdates   map[string]string `json:"context_updates,omitempty"`
	Notes            string            `json:"notes,omitempty"`
	FailureReason    string            `json:"failure_reason,omitempty"`
	// FailureClass distinguishes machinery failures from task failures
	// (loop-guards LG1). Empty means task.
	FailureClass string `json:"failure_class,omitempty"`
	NextNode     string `json:"-"`
}

// Finalize fills derived fields just before serialization or
// engine-internal handling.
func (o *Outcome) Finalize() {
	o.StatusString = o.Status.String()
	if o.ContextUpdates == nil {
		o.ContextUpdates = map[string]string{}
	}
}

// Context is the pipeline-wide key-value store (spec §5.1). It is safe
// for concurrent access; the engine itself is single-threaded but
// parallel handlers may share the same instance.
type Context struct {
	mu     sync.RWMutex
	values map[string]string
	logs   []string
}

// NewContext returns an empty Context.
func NewContext() *Context {
	return &Context{values: map[string]string{}}
}

// Set writes a key.
func (c *Context) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = value
}

// Get returns the value for key (empty if missing).
func (c *Context) Get(key string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.values[key]
}

// Lookup returns the value for key and whether it is present. It lets
// callers distinguish a missing key from a present-but-empty value —
// used by runtime interpolation to fail fast on undefined keys.
func (c *Context) Lookup(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.values[key]
	return v, ok
}

// Apply merges a batch of updates.
func (c *Context) Apply(updates map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range updates {
		c.values[k] = v
	}
}

// Snapshot returns a deep copy of the current key-value store and logs.
func (c *Context) Snapshot() (map[string]string, []string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	values := make(map[string]string, len(c.values))
	for k, v := range c.values {
		values[k] = v
	}
	logs := append([]string{}, c.logs...)
	return values, logs
}

// Clone returns a deep copy suitable for isolating parallel branches.
func (c *Context) Clone() *Context {
	values, logs := c.Snapshot()
	return &Context{values: values, logs: logs}
}

// AppendLog records a free-form log entry on the context.
func (c *Context) AppendLog(entry string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, entry)
}

// MirrorGraph copies graph-level attributes into the context's `graph.*`
// namespace at run start (spec §5.1 built-in keys).
func (c *Context) MirrorGraph(g *graph.Graph) {
	for k, v := range g.Attrs {
		c.Set("graph."+k, v)
	}
	c.Set("graph.goal", g.Goal())
}

// Resolver implements condition.Resolver against an outcome+context
// pair. Resolution rules follow spec §10.4:
//   - outcome → last handler status
//   - preferred_label → outcome.PreferredLabel
//   - context.* → context.Get(key) with fallback to bare key
type Resolver struct {
	Outcome *Outcome
	Context *Context
}

// Resolve returns a string value for the given key per spec §10.4.
func (r Resolver) Resolve(key string) string {
	if r.Outcome != nil {
		if key == "outcome" {
			return r.Outcome.Status.String()
		}
		if key == "preferred_label" {
			return r.Outcome.PreferredLabel
		}
	}
	if r.Context == nil {
		return ""
	}
	if v := r.Context.Get(key); v != "" {
		return v
	}
	if len(key) > 8 && key[:8] == "context." {
		if v := r.Context.Get(key[8:]); v != "" {
			return v
		}
	}
	return ""
}

// Checkpoint is a serializable execution snapshot (spec §5.3) written
// to `{logs_root}/checkpoint.json` after each node completes.
type Checkpoint struct {
	Timestamp      time.Time          `json:"timestamp"`
	CurrentNode    string             `json:"current_node"`
	CompletedNodes []string           `json:"completed_nodes"`
	NodeRetries    map[string]int     `json:"node_retries"`
	ContextValues  map[string]string  `json:"context"`
	Logs           []string           `json:"logs"`
	LastOutcome    *Outcome           `json:"last_outcome,omitempty"`
	NodeOutcomes   map[string]Outcome `json:"node_outcomes"`
}

// Manifest is run-level metadata (spec §5.6) written once at start to the
// engine's identity record run.json (ui-run-view-v3 P5a — the daemon owns the
// sibling manifest.json).
type Manifest struct {
	RunID     string    `json:"run_id"`
	GraphName string    `json:"graph_name"`
	Goal      string    `json:"goal"`
	StartedAt time.Time `json:"started_at"`
}
