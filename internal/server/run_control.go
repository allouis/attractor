package server

import "net/http"

// restartPipeline serves POST /pipelines/{id}/restart: re-run a failed run from
// its checkpoint (web-ui-v2-spec U6, re-run-from-failure). It resets the run's
// terminal state and re-enqueues it; the engine resumes from the last completed
// node, re-executing the one that failed. A run that is not a resumable failure
// — not failed, reloaded from disk after a daemon restart, or run on a
// local/vm launcher whose checkpoint died with the child — is a 409; an unknown
// run is a 404. Cancel and fresh re-run are separate: cancel already shipped,
// and a fresh re-run resubmits via POST /workflows/{name}/run.
func (s *Server) restartPipeline(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !run.prepareRestart() {
		http.Error(w, "run cannot be resumed from failure", http.StatusConflict)
		return
	}
	// Re-derive the static checks from LIVE config before re-launch: checks are
	// operator policy, not run identity, so a revived run must run the operator's
	// current check commands even if they changed since the run was submitted.
	s.reseedChecks(run)
	s.dispatcher.enqueue(run)
	writeJSON(w, http.StatusAccepted, map[string]any{"id": run.ID, "restarted": true})
}
