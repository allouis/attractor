package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/fabro/attractor/internal/automation"
)

// listAutomations returns the loaded automations for the UI's automation
// list / manual-run buttons (service-spec §5).
func (s *Server) listAutomations(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	autos := s.automations
	s.mu.RUnlock()
	out := make([]map[string]any, 0, len(autos))
	for _, a := range autos {
		out = append(out, map[string]any{
			"name":     a.Name,
			"pipeline": a.Pipeline,
			"cwd":      a.Cwd,
			"cron":     a.Cron,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"automations": out})
}

// runAutomation fires one automation manually (POST /automations/{name}/run,
// the UI button). The automation is re-read from disk by name so on-disk
// edits are picked up without a daemon restart — our lightweight stand-in
// for directory watching (service-spec §5).
func (s *Server) runAutomation(w http.ResponseWriter, r *http.Request) {
	a, err := s.lookupAutomation(r.PathValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	id, err := s.fireAutomation(a)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

// lookupAutomation re-reads and parses <dir>/<name>.toml. The name is
// confined to a single path segment so a request cannot escape the
// automations directory.
func (s *Server) lookupAutomation(name string) (automation.Automation, error) {
	if s.automationsDir == "" || name == "" ||
		strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return automation.Automation{}, fmt.Errorf("automation %q not found", name)
	}
	data, err := os.ReadFile(filepath.Join(s.automationsDir, name+".toml"))
	if err != nil {
		return automation.Automation{}, fmt.Errorf("automation %q not found", name)
	}
	return automation.Parse(name, data)
}

// fireAutomation resolves the automation's pipeline .dot and submits it
// through the shared admission path, so a scheduled or manual automation
// enqueues exactly like a POST /pipelines. The pipeline reference must be
// a filesystem path (bare-name pipeline lookup is a CLI-only convenience);
// a leading ~ is expanded to the home directory.
func (s *Server) fireAutomation(a automation.Automation) (string, error) {
	src, err := os.ReadFile(expandTilde(a.Pipeline))
	if err != nil {
		return "", fmt.Errorf("automation %q: read pipeline: %w", a.Name, err)
	}
	return s.submit(string(src), a.Vars, a.Cwd, nil)
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
