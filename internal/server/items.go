package server

import (
	"net/http"

	"github.com/fabro/attractor/internal/source"
)

// linkedRun is the run-side annotation attached to an Item: which runs
// carry its item_ref and their status (items-spec §11).
type linkedRun struct {
	ID     string    `json:"id"`
	Status RunStatus `json:"status"`
}

// annotatedItem is a source Item enriched with its linked-run state. The
// embedded Item's JSON fields (ref, title, url, vars) promote inline.
type annotatedItem struct {
	source.Item
	LinkedRuns []linkedRun `json:"linked_runs,omitempty"`
	InProgress bool        `json:"in_progress"`
}

// listItems fetches Items from a named Source and annotates each with the
// runs it has spawned (items-spec §11: GET /items?source=…&filter=…). The
// daemon owns sources; TUI/CLI are clients.
func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("source")
	src, ok := s.sources[name]
	if !ok {
		http.Error(w, "unknown source "+name, http.StatusBadRequest)
		return
	}
	filter := source.Filter{Assigned: r.URL.Query().Get("filter") == "assigned"}
	items, err := src.List(r.Context(), filter)
	if err != nil {
		http.Error(w, "fetch items: "+err.Error(), http.StatusBadGateway)
		return
	}
	out := make([]annotatedItem, 0, len(items))
	for _, it := range items {
		out = append(out, s.annotate(it))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// annotate marks an Item with its linked runs. In-progress is derived,
// never stored: an Item is in-progress iff a linked run is queued or
// running (items-spec §"In-progress").
func (s *Server) annotate(it source.Item) annotatedItem {
	ann := annotatedItem{Item: it}
	for _, run := range s.registry.RunsForItem(it.Ref) {
		status := run.Status()
		ann.LinkedRuns = append(ann.LinkedRuns, linkedRun{ID: run.ID, Status: status})
		if status == RunQueued || status == RunRunning {
			ann.InProgress = true
		}
	}
	return ann
}
