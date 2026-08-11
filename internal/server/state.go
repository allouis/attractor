package server

import (
	"net/http"
	"time"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/items"
	"github.com/allouis/attractor/internal/setup"
)

// pipelineState is the server-computed run-state document (ui-run-view-v3 P1).
// It is derived once, server-side, from data the daemon holds for every runner
// (registry status + manifest, event history, and the run's source graph) — the
// UI hydrates from it rather than replaying the SSE stream client-side. The
// active_nodes set is fail-aware: a stage that reached a fail terminal and
// routed elsewhere is not active, which naive started−completed set algebra
// (completed counts only stage_completed events) gets wrong.
type pipelineState struct {
	ID           string               `json:"id"`
	Status       RunStatus            `json:"status"`
	Repo         string               `json:"repo,omitempty"`
	WorkflowName string               `json:"workflow_name,omitempty"`
	Item         *stateItem           `json:"item,omitempty"`
	Placement    statePlacement       `json:"placement"`
	StartedAt    string               `json:"started_at,omitempty"`
	CompletedAt  string               `json:"completed_at,omitempty"`
	ActiveNodes  []string             `json:"active_nodes"`
	Nodes        map[string]nodeState `json:"nodes"`
	Questions    []map[string]any     `json:"questions"`
}

// stateItem is the external work item that spawned the run: its human-readable
// identifier/title/url (from the seeded vars) and the source it came from (from
// the item_ref tag).
type stateItem struct {
	Identifier string `json:"identifier,omitempty"`
	Title      string `json:"title,omitempty"`
	URL        string `json:"url,omitempty"`
	Source     string `json:"source,omitempty"`
}

// statePlacement names where the run executes. Runner is never empty: the
// daemon's default in-process launcher reports as "direct".
type statePlacement struct {
	Runner string `json:"runner"`
	Image  string `json:"image,omitempty"`
}

// nodeState is one node's derived status. Every node of the source graph
// appears, pending ones included. Status is pending/running/completed/failed;
// Outcome is the last terminal event's status string where known.
type nodeState struct {
	Status      string `json:"status"`
	Attempts    int    `json:"attempts,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
}

// getRunState serves GET /pipelines/{id}/state.
func (s *Server) getRunState(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, http.StatusOK, run.State())
}

// State computes the run's state document. It reads the event history, the
// pending questions, and the run's scalar fields under the lock, then derives
// the fail-aware active set and per-node statuses.
func (r *Run) State() pipelineState {
	active, nodes := r.deriveNodes()
	questions := r.PendingQuestions()
	if questions == nil {
		questions = []map[string]any{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	st := pipelineState{
		ID:           r.ID,
		Status:       r.status,
		Repo:         r.repo,
		WorkflowName: r.workflowName,
		Placement:    statePlacement{Runner: placementRunner(r.placement), Image: r.image},
		ActiveNodes:  active,
		Nodes:        nodes,
		Questions:    questions,
	}
	if !r.startedAt.IsZero() {
		st.StartedAt = r.startedAt.Format(time.RFC3339Nano)
	}
	if !r.completedAt.IsZero() {
		st.CompletedAt = r.completedAt.Format(time.RFC3339Nano)
	}
	st.Item = itemState(r.itemRef, r.initialContext)
	return st
}

// placementRunner maps an empty placement — the daemon's default in-process
// launcher (server New) — to its runner name "direct".
func placementRunner(placement string) string {
	if placement == "" {
		return "direct"
	}
	return placement
}

// itemState assembles the item block from the run's item_ref tag (source +
// external id) and the seeded vars (the human-readable identifier/title/url the
// item source mapped in). Returns nil when no item spawned the run.
func itemState(itemRef string, ctx map[string]string) *stateItem {
	if itemRef == "" {
		return nil
	}
	it := &stateItem{
		Identifier: ctx["identifier"],
		Title:      ctx["title"],
		URL:        ctx["url"],
	}
	if ref, err := items.ParseItemRef(itemRef); err == nil {
		it.Source = ref.Source
		if it.Identifier == "" {
			it.Identifier = ref.ExternalID
		}
	}
	return it
}

// deriveNodes walks the run's event history to derive the fail-aware active
// set and each node's status. Every node of the source graph seeds the map as
// pending, so yet-to-run nodes are reported too. A node is active when it has
// a stage_started with no matching terminal (stage_completed or stage_failed) —
// counting stage_failed as terminal is what makes the set fail-aware. Attempts
// is the stage_started count (a retry re-emits stage_started).
func (r *Run) deriveNodes() ([]string, map[string]nodeState) {
	nodes := map[string]nodeState{}
	if g := r.resolvedGraph(); g != nil {
		for _, id := range g.NodeOrder {
			nodes[id] = nodeState{Status: "pending"}
		}
	}
	var order []string
	seen := map[string]bool{}
	for _, ev := range r.historySnapshot() {
		// Forwarded child events belong to a nested run, not this graph.
		if isChildEvent(ev) || ev.NodeID == "" {
			continue
		}
		if !seen[ev.NodeID] {
			seen[ev.NodeID] = true
			order = append(order, ev.NodeID)
		}
		ns := nodes[ev.NodeID]
		ts := ev.Timestamp.Format(time.RFC3339Nano)
		switch ev.Kind {
		case engine.EventStageStarted:
			ns.Status = "running"
			ns.Attempts++
			if ns.StartedAt == "" {
				ns.StartedAt = ts
			}
			// A re-entry clears any prior terminal so the node reads as
			// running again (a fail-then-retry route re-runs the node).
			ns.CompletedAt = ""
			ns.Outcome = ""
		case engine.EventStageCompleted:
			ns.Status = "completed"
			if statusIsFail(ev.Status) {
				ns.Status = "failed"
			}
			ns.Outcome = ev.Status
			ns.CompletedAt = ts
		case engine.EventStageFailed:
			ns.Status = "failed"
			if ev.Status != "" {
				ns.Outcome = ev.Status
			} else {
				ns.Outcome = engine.StatusFail.String()
			}
			ns.CompletedAt = ts
		default:
			continue // stage_retrying, usage, etc. do not change node status
		}
		nodes[ev.NodeID] = ns
	}
	active := []string{}
	for _, id := range order {
		if nodes[id].Status == "running" {
			active = append(active, id)
		}
	}
	return active, nodes
}

// statusIsFail reports whether a status string is the fail terminal. It keeps
// active-set derivation fail-aware even if a fail ever arrives on a
// stage_completed event rather than stage_failed.
func statusIsFail(status string) bool {
	return engine.ParseStatus(status) == engine.StatusFail
}

// historySnapshot returns the run's effective event history: the in-memory
// buffer when present, else the disk replay (a run reloaded after a daemon
// restart), repaired for an interrupted tail. Copied under the lock so a
// concurrent fanOutEvents append does not race the read.
func (r *Run) historySnapshot() []engine.Event {
	r.mu.RLock()
	inMemory := append([]engine.Event(nil), r.history...)
	r.mu.RUnlock()
	return r.reconstructHistory(inMemory)
}

// resolvedGraph returns the run's graph with @file prompts already resolved.
// A live run holds the prepared graph in memory (setup.Prepare inlined the
// prompts before the run started); a run reloaded from disk has none, so it is
// re-prepared from the stored source against the run's base dir (cwd fallback),
// dropping to an unresolved build only if preparation fails.
func (r *Run) resolvedGraph() *graph.Graph {
	r.mu.RLock()
	g := r.graph
	r.mu.RUnlock()
	if g != nil {
		return g
	}
	src := r.Source()
	if src == "" {
		return nil
	}
	base := r.baseDir()
	if base == "" {
		base = r.Cwd()
	}
	if pg, err := setup.Prepare(setup.Options{Source: src, BaseDir: base, Cwd: r.Cwd()}); err == nil {
		return pg.Graph
	}
	if file, err := dot.Parse(src); err == nil {
		if built, err := graph.Build(file); err == nil {
			return built
		}
	}
	return nil
}

// writeJSONError writes an {"error": msg} body with the given status, the JSON
// error shape the state/node endpoints return for unknown runs and nodes.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}
