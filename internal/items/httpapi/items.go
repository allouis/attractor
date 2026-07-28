// Package httpapi is the items-spec HTTP surface (§11): GET /items and
// POST /items/run. It is the only place the typed items.ItemRef meets the
// wire — the core engine and server carry the run's item link as an opaque
// tag. Register mounts the routes on a caller's mux against a Deps the
// server implements, so the daemon owns sources/repos/admission while this
// package owns the item↔tag mapping and annotation.
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/allouis/attractor/internal/items"
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

// Deps is the server-side surface the items handlers need: named sources,
// repo→checkout mapping, the shared admission path, and the runs linked to
// an item tag. Keeping it an interface leaves the engine/server free of any
// items knowledge beyond the opaque tag.
type Deps interface {
	// Source returns the named Source, ok=false if unknown.
	Source(name string) (source.Source, bool)
	// RepoPath maps `owner/name` to a local checkout, ok=false if unmapped.
	RepoPath(repo string) (string, bool)
	// Submit runs the shared admission path, stamping the run with the
	// opaque item tag, and returns the new run id.
	Submit(dot string, vars map[string]string, cwd, tag string) (string, error)
	// LinkedRuns returns the runs stamped with the item tag, newest first.
	LinkedRuns(tag string) []LinkedRun
}

// Register mounts GET /items and POST /items/run on mux (items-spec §11).
func Register(mux *http.ServeMux, deps Deps) {
	h := &handlers{deps: deps}
	mux.HandleFunc("GET /items", h.listItems)
	mux.HandleFunc("POST /items/run", h.runItem)
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

// runItemRequest is the POST /items/run body (items-spec I4): the human
// supplies the item, the workflow, and (for non-PR items) the repo.
type runItemRequest struct {
	ItemRef  items.ItemRef `json:"item_ref"`
	Pipeline string        `json:"pipeline"`
	Repo     string        `json:"repo"`
}

// runItem dispatches a single Item to a chosen pipeline (items-spec I4:
// MVP, no routing). It resolves the item → vars via its Source, the repo
// → a local checkout for the run's cwd, stamps item_ref, and starts the
// run through the shared admission path. A PR item auto-fills its repo;
// any other item takes the repo from the request.
func (h *handlers) runItem(w http.ResponseWriter, r *http.Request) {
	var req runItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.ItemRef.Source == "" || req.ItemRef.Type == "" || req.ItemRef.ExternalID == "" {
		http.Error(w, "item_ref requires source, type, and external_id", http.StatusBadRequest)
		return
	}
	src, ok := h.deps.Source(req.ItemRef.Source)
	if !ok {
		http.Error(w, "unknown source "+req.ItemRef.Source, http.StatusBadRequest)
		return
	}
	item, err := src.Get(r.Context(), req.ItemRef)
	if err != nil {
		http.Error(w, "resolve item: "+err.Error(), http.StatusBadGateway)
		return
	}
	cwd, err := h.resolveRepoPath(item, req.Repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	dot, err := os.ReadFile(expandTilde(req.Pipeline))
	if err != nil {
		http.Error(w, "read pipeline: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	id, err := h.deps.Submit(string(dot), item.Vars, cwd, req.ItemRef.String())
	if err != nil {
		http.Error(w, "validate: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// resolveRepoPath picks the repo (item's `repo` var — a PR auto-fills it —
// else the request's repo) and maps it to a local checkout. An unset or
// unmapped repo is a 422: attractor can't set the run's cwd.
func (h *handlers) resolveRepoPath(item source.Item, reqRepo string) (string, error) {
	repo := item.Vars["repo"]
	if repo == "" {
		repo = reqRepo
	}
	if repo == "" {
		return "", fmt.Errorf("no repo: item carries no repo and none supplied in the request")
	}
	path, ok := h.deps.RepoPath(repo)
	if !ok {
		return "", fmt.Errorf("repo %q not mapped in repos.toml", repo)
	}
	return path, nil
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

// expandTilde replaces a leading ~ with the user's home directory.
func expandTilde(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}
