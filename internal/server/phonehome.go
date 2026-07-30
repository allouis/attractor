package server

import (
	"encoding/json"
	"net/http"

	"github.com/allouis/attractor/internal/engine"
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
	var ev engine.Event
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}
	run.Ingest(ev)
	w.WriteHeader(http.StatusNoContent)
}
