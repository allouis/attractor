package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/fabro/attractor/internal/engine"
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

// runItemRequest is the POST /items/run body (items-spec I4): the human
// supplies the item, the workflow, and (for non-PR items) the repo.
type runItemRequest struct {
	ItemRef  engine.ItemRef `json:"item_ref"`
	Pipeline string         `json:"pipeline"`
	Repo     string         `json:"repo"`
}

// runItem dispatches a single Item to a chosen pipeline (items-spec I4:
// MVP, no routing). It resolves the item → vars via its Source, the repo
// → a local checkout for the run's cwd, stamps item_ref, and starts the
// run through the shared admission path. A PR item auto-fills its repo;
// any other item takes the repo from the request.
func (s *Server) runItem(w http.ResponseWriter, r *http.Request) {
	var req runItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.ItemRef.Source == "" || req.ItemRef.Type == "" || req.ItemRef.ExternalID == "" {
		http.Error(w, "item_ref requires source, type, and external_id", http.StatusBadRequest)
		return
	}
	src, ok := s.sources[req.ItemRef.Source]
	if !ok {
		http.Error(w, "unknown source "+req.ItemRef.Source, http.StatusBadRequest)
		return
	}
	item, err := src.Get(r.Context(), req.ItemRef)
	if err != nil {
		http.Error(w, "resolve item: "+err.Error(), http.StatusBadGateway)
		return
	}
	cwd, err := s.resolveRepoPath(item, req.Repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	dot, err := os.ReadFile(expandTilde(req.Pipeline))
	if err != nil {
		http.Error(w, "read pipeline: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	id, err := s.submit(string(dot), item.Vars, cwd, &req.ItemRef)
	if err != nil {
		http.Error(w, "validate: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// resolveRepoPath picks the repo (item's `repo` var — a PR auto-fills it —
// else the request's repo) and maps it to a local checkout. An unset or
// unmapped repo is a 422: attractor can't set the run's cwd.
func (s *Server) resolveRepoPath(item source.Item, reqRepo string) (string, error) {
	repo := item.Vars["repo"]
	if repo == "" {
		repo = reqRepo
	}
	if repo == "" {
		return "", fmt.Errorf("no repo: item carries no repo and none supplied in the request")
	}
	path, ok := s.repos.Path(repo)
	if !ok {
		return "", fmt.Errorf("repo %q not mapped in repos.toml", repo)
	}
	return path, nil
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
