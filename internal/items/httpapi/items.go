// Package httpapi is the items-spec HTTP surface (§11): GET /items and
// GET /items/sources. It reads Items from the daemon's sources and annotates
// each with its linked runs. Launching a run from an item is the server's
// concern (POST /workflows/{name}/run with an item_ref, run-workflow-spec R4).
// Register mounts the routes on a caller's mux against a Deps the server
// implements, so the daemon owns sources while this package owns annotation.
package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/allouis/attractor/internal/items/source"
)

// LinkedRun is a run stamped with an item's tag, projected for annotation:
// its id, status string, and whether it keeps the item in-progress (queued
// or running). The server maps its registry runs onto this shape.
type LinkedRun struct {
	ID     string
	Status string
	Active bool
}

// Deps is the server-side surface the items handlers need: named sources and
// the runs linked to an item tag. Keeping it an interface leaves the
// engine/server free of any items knowledge beyond the opaque tag.
type Deps interface {
	// Source returns the named Source, ok=false if unknown.
	Source(name string) (source.Source, bool)
	// SourceNames lists the configured source names (any order). Backs GET
	// /items/sources so the UI discovers what to fetch instead of hardcoding
	// the list across the API boundary (web-ui-spec W3).
	SourceNames() []string
	// LinkedRuns returns the runs stamped with the item tag, newest first.
	LinkedRuns(tag string) []LinkedRun
}

// Register mounts GET /items and GET /items/sources on mux (items-spec §11).
func Register(mux *http.ServeMux, deps Deps) {
	h := &handlers{deps: deps}
	mux.HandleFunc("GET /items", h.listItems)
	mux.HandleFunc("GET /items/sources", h.listSources)
}

type handlers struct {
	deps Deps
}

// linkedRun is the run-side annotation attached to an Item: which runs
// carry its item_ref and their status (items-spec §11).
type linkedRun struct {
	ID     string `json:"id"`
	Status string `json:"status"`
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
func (h *handlers) listItems(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("source")
	src, ok := h.deps.Source(name)
	if !ok {
		http.Error(w, "unknown source "+name, http.StatusBadRequest)
		return
	}
	filter := source.Filter{Assigned: r.URL.Query().Get("filter") == "assigned"}
	list, err := src.List(r.Context(), filter)
	if err != nil {
		http.Error(w, "fetch items: "+err.Error(), http.StatusBadGateway)
		return
	}
	out := make([]annotatedItem, 0, len(list))
	for _, it := range list {
		out = append(out, h.annotate(it))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// listSources serves GET /items/sources: the configured source names,
// sorted, so the UI fetches exactly the sources the daemon has rather than
// a hardcoded list (web-ui-spec W3). An unconfigured daemon returns [].
func (h *handlers) listSources(w http.ResponseWriter, r *http.Request) {
	names := h.deps.SourceNames()
	if names == nil {
		names = []string{}
	}
	sort.Strings(names)
	writeJSON(w, http.StatusOK, map[string]any{"sources": names})
}

// annotate marks an Item with its linked runs. In-progress is derived,
// never stored: an Item is in-progress iff a linked run is queued or
// running (items-spec §"In-progress").
func (h *handlers) annotate(it source.Item) annotatedItem {
	ann := annotatedItem{Item: it}
	for _, run := range h.deps.LinkedRuns(it.Ref.String()) {
		ann.LinkedRuns = append(ann.LinkedRuns, linkedRun{ID: run.ID, Status: run.Status})
		if run.Active {
			ann.InProgress = true
		}
	}
	return ann
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
