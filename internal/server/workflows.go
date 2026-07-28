package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
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
		if !e.IsDir() {
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
