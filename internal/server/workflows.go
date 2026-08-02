package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/graph"
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

// workflow is one catalog entry: the definition's directory name and the
// absolute path to its pipeline.dot, which POST /items/run wants as its
// `pipeline` field (web-ui-spec §Views).
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
	if r.URL.Query().Get("expand") != "" {
		svg, err = render.SVGExpanded(source, dir)
	} else {
		svg, err = render.SVG(source)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write(svg)
}
