package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

// getWorkflowGraph serves GET /workflows/{name}/graph: the definition's
// pipeline.dot rendered to SVG via render.SVG — the same renderer as the
// run-graph endpoint, but from the catalog file with no run needed
// (web-ui-spec W2). The name is confined to the catalog root so it cannot
// traverse out, mirroring getArtifact.
func (s *Server) getWorkflowGraph(w http.ResponseWriter, r *http.Request) {
	root := filepath.Clean(s.workflowsDir)
	dir := filepath.Join(root, filepath.Clean("/"+r.PathValue("name")))
	if dir != root && !strings.HasPrefix(dir, root+string(filepath.Separator)) {
		http.NotFound(w, r)
		return
	}
	source, err := os.ReadFile(filepath.Join(dir, "pipeline.dot"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	svg, err := render.SVG(source)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write(svg)
}
