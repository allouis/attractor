package server

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/allouis/attractor/internal/config"
)

// getConfig returns the whole daemon-owned config, secrets redacted: the
// Linear key is reported as api_key_set only, never echoed
// (config-screen-spec). A missing file yields the fresh default.
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	doc, err := loadConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, doc.Redacted())
}

// putConfig replaces the whole document. It secret-merges the incoming doc
// over the stored one (an empty api_key keeps the stored key), validates
// (reject unknown backend / dangling default_provider, warn on unresolved
// repo paths), then persists. Structural rejections save nothing (422);
// soft warnings save and are returned to the caller.
func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var incoming config.Document
	if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
		http.Error(w, "malformed config document", http.StatusBadRequest)
		return
	}
	prev, err := loadConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	merged := incoming.WithMergedSecrets(prev)
	warnings, err := merged.Validate()
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": err.Error()})
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := merged.Save(home); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"warnings": warnings})
}

// listRepos projects the registered repos onto a name-sorted list, read
// live from config.json so UI edits show without a restart — the run-form
// repo dropdown source (run-workflow-spec R2).
func (s *Server) listRepos(w http.ResponseWriter, r *http.Request) {
	doc, err := loadConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, doc.ReposList())
}

// loadConfig reads the daemon-owned config.json from the user's home,
// mirroring seedChecks' live-load seam.
func loadConfig() (config.Document, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return config.Document{}, err
	}
	return config.LoadDocument(home)
}
