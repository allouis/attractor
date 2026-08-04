package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/items"
	"github.com/allouis/attractor/internal/render"
)

// defaultWorkflowsDir is the catalog root used when Config.WorkflowsDir is
// empty: ~/.attractor/pipelines (web-ui-spec W2). Falls back to a relative
// path when the home directory cannot be determined.
func defaultWorkflowsDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".attractor", "pipelines")
	}
	return filepath.Join(".attractor", "pipelines")
}

// workflow is one catalog entry: the definition's directory name (the handle
// the run form keys POST /workflows/{name}/run on) and the absolute path to
// its pipeline.dot (web-ui-spec §Views).
type workflow struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// listWorkflows serves GET /workflows: every subdirectory of the catalog
// root that holds a pipeline.dot, name-sorted. A missing root yields an
// empty list rather than an error, matching the repos/automations
// missing-file tolerance.
func (s *Server) listWorkflows(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(s.workflowsDir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, []workflow{})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := []workflow{}
	for _, e := range entries {
		// Stat (not DirEntry.IsDir) so a symlinked workflow directory
		// counts — users commonly symlink their repo's pipelines into the
		// catalog root rather than copy them.
		if info, err := os.Stat(filepath.Join(s.workflowsDir, e.Name())); err != nil || !info.IsDir() {
			continue
		}
		def := filepath.Join(s.workflowsDir, e.Name(), "pipeline.dot")
		if info, err := os.Stat(def); err != nil || info.IsDir() {
			continue
		}
		out = append(out, workflow{Name: e.Name(), Path: def})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

// workflowDir resolves a catalog workflow name to its directory, confined to
// the catalog root so a name cannot traverse out (mirroring getArtifact). The
// bool is false when the name escapes the root — callers answer 404.
func (s *Server) workflowDir(name string) (string, bool) {
	root := filepath.Clean(s.workflowsDir)
	dir := filepath.Join(root, filepath.Clean("/"+name))
	if dir != root && !strings.HasPrefix(dir, root+string(filepath.Separator)) {
		return "", false
	}
	return dir, true
}

// workflowDetail is the input contract GET /workflows/{name} exposes: the
// catalog entry plus the goal and declared vars parsed from its pipeline.dot,
// so the Run modal can build one form field per var (run-workflow-spec R1).
type workflowDetail struct {
	Name string   `json:"name"`
	Path string   `json:"path"`
	Goal string   `json:"goal"`
	Vars []string `json:"vars"`
}

// getWorkflow serves GET /workflows/{name}: the workflow's goal and declared
// vars, parsed from the catalog pipeline.dot via graph.Build. A missing
// definition is a 404; an unparseable one is a 500 (broken, not absent).
func (s *Server) getWorkflow(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.workflowDir(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(dir, "pipeline.dot")
	source, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, err := dot.Parse(string(source))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	g, err := graph.Build(file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	vars := g.DeclaredVars()
	if vars == nil {
		vars = []string{}
	}
	writeJSON(w, http.StatusOK, workflowDetail{
		Name: r.PathValue("name"),
		Path: path,
		Goal: g.Goal(),
		Vars: vars,
	})
}

// runWorkflowRequest is the POST /workflows/{name}/run body: the chosen repo
// (the run's cwd resolver and the `repo` var), the declared vars filled in the
// form, and an optional item_ref that prefills them from an external Item
// (run-workflow-spec R3). Item-driven and standalone launches are the same
// path, with or without item_ref.
type runWorkflowRequest struct {
	Repo    string            `json:"repo"`
	Vars    map[string]string `json:"vars"`
	ItemRef *items.ItemRef    `json:"item_ref"`
}

// runWorkflow serves POST /workflows/{name}/run: the single admission point for
// a manual launch. It resolves the catalog workflow (baseDir = its dir, so
// @prompt refs and any manager_loop child_dotfile resolve against it), prefills
// vars from item_ref when present, overlays the request vars, resolves the
// registered repo to the run's cwd, and submits through the shared path (which
// seeds the run context and static checks). The single admission point for a
// manual launch — item-driven and standalone both funnel through here.
func (s *Server) runWorkflow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	dir, ok := s.workflowDir(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	dotSource, err := os.ReadFile(filepath.Join(dir, "pipeline.dot"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var req runWorkflowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Base vars come from the linked Item (each declared var prefilled); the
	// request vars overlay them so a human can tweak before launching.
	vars := map[string]string{}
	tag := ""
	if req.ItemRef != nil {
		if req.ItemRef.Source == "" || req.ItemRef.Type == "" || req.ItemRef.ExternalID == "" {
			http.Error(w, "item_ref requires source, type, and external_id", http.StatusBadRequest)
			return
		}
		src, ok := s.sources[req.ItemRef.Source]
		if !ok {
			http.Error(w, "unknown source "+req.ItemRef.Source, http.StatusBadRequest)
			return
		}
		item, err := src.Get(r.Context(), *req.ItemRef)
		if err != nil {
			http.Error(w, "resolve item: "+err.Error(), http.StatusBadGateway)
			return
		}
		for k, v := range item.Vars {
			vars[k] = v
		}
		tag = req.ItemRef.String()
	}
	for k, v := range req.Vars {
		vars[k] = v
	}

	// The repo field is authoritative (the form's dropdown), falling back to a
	// prefilled item repo. It is both the cwd resolver and the `repo` var, so
	// seed it back so the run context and per-repo checks agree.
	repo := req.Repo
	if repo == "" {
		repo = vars["repo"]
	}
	if repo == "" {
		http.Error(w, "no repo: none chosen and none prefilled from the item", http.StatusUnprocessableEntity)
		return
	}
	vars["repo"] = repo
	// Resolve cwd from live config, the same source the dropdown (GET /repos)
	// and the run's checks/placement read — never a boot snapshot — so a repo
	// added via the config screen is runnable without a daemon restart.
	doc, err := loadConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cwd, ok := doc.RepoPath(repo)
	if !ok {
		http.Error(w, "repo "+repo+" not mapped in config", http.StatusUnprocessableEntity)
		return
	}

	id, err := s.submit(string(dotSource), vars, cwd, repo, tag, name, dir, "", "")
	if err != nil {
		http.Error(w, "validate: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// getWorkflowGraph serves GET /workflows/{name}/graph: the definition's
// pipeline.dot rendered to SVG via render.SVG — the same renderer as the
// run-graph endpoint, but from the catalog file with no run needed
// (web-ui-spec W2).
func (s *Server) getWorkflowGraph(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.workflowDir(r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	source, err := os.ReadFile(filepath.Join(dir, "pipeline.dot"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// ?expand=1 inlines each stack.manager_loop node's child pipeline as a
	// nested cluster, resolving relative child_dotfile paths against the
	// workflow's own directory.
	var svg []byte
	engine := graphEngine(r)
	if r.URL.Query().Get("expand") != "" {
		svg, err = render.SVGExpanded(source, dir, engine)
	} else {
		svg, err = render.SVG(source, engine)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write(svg)
}
