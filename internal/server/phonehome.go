package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/version"
)

// runTokenValid reports whether the request carries the run's phone-home
// token as a bearer credential. This gates the reporting endpoints
// (events/control/artifacts) independently of the global --auth-token, so
// a launched child (local subprocess or VM) authenticates as exactly the
// run it was started for and nothing else.
func runTokenValid(r *http.Request, run *Run) bool {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) <= len(prefix) || auth[:len(prefix)] != prefix {
		return false
	}
	return auth[len(prefix):] == run.Token()
}

// ingestEvent accepts one engine.Event reported by a phone-home child and
// folds it into the run's history, SSE fan-out, and events.jsonl.
func (s *Server) ingestEvent(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !runTokenValid(r, run) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	run.warnRevSkew(r.Header.Get(version.RevisionHeader))
	var ev engine.Event
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}
	run.Ingest(ev)
	w.WriteHeader(http.StatusNoContent)
}

// warnRevSkew logs once per run when a phone-home child advertises a build
// (git revision) different from the daemon's — the signature of a stale VM
// image or subprocess (rebuild .#vm-runner). Skips when either side's
// revision is unknown (nothing to compare).
func (r *Run) warnRevSkew(childRev string) {
	daemon := version.Get()
	if childRev == "" || childRev == "unknown" || daemon == "unknown" || childRev == daemon {
		return
	}
	r.mu.Lock()
	if r.revWarned {
		r.mu.Unlock()
		return
	}
	r.revWarned = true
	r.mu.Unlock()
	log.Printf("attractor: run %s: child build %s != daemon %s — the VM/subprocess runs a different attractor; rebuild the VM image (nix build .#vm-runner)", r.ID, childRev, daemon)
}

// controlAnswer is one human-gate answer delivered to a polling child.
type controlAnswer struct {
	Key   string `json:"key,omitempty"`
	Label string `json:"label,omitempty"`
	Text  string `json:"text,omitempty"`
}

// controlResponse is the daemon→child control channel: whether to abort
// and any answers to questions the child previously asked (keyed by
// question id). The child polls GET /control since the daemon cannot
// push to it over a network boundary.
type controlResponse struct {
	Cancel  bool                     `json:"cancel"`
	Answers map[string]controlAnswer `json:"answers,omitempty"`
}

// control returns the run's pending control state for a polling child.
func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !runTokenValid(r, run) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, controlResponse{
		Cancel:  run.IsCancelled(),
		Answers: run.polledAnswers(),
	})
}

// putArtifact stores an artifact uploaded by a phone-home child under the
// run's logs root, so GET /artifacts and GET /stages serve it identically
// to an in-process run's on-disk stage files. The path is confined to the
// logs root (same guard as getArtifact).
func (s *Server) putArtifact(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !runTokenValid(r, run) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	root := filepath.Clean(run.logsRoot)
	full := filepath.Join(root, filepath.Clean("/"+r.PathValue("path")))
	if full == root || !strings.HasPrefix(full, root+string(filepath.Separator)) {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		http.Error(w, "mkdir", http.StatusInternalServerError)
		return
	}
	f, err := os.Create(full)
	if err != nil {
		http.Error(w, "create", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	if _, err := io.Copy(f, r.Body); err != nil {
		http.Error(w, "write", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
