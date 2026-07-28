package server

import "testing"

// TestSummaryNeedsHuman checks the run summary exposes a needs_human flag
// derived from pending wait.human questions — the load-bearing signal the
// fleet view counts and flags (web-ui-spec W5). It is orthogonal to status:
// a running run blocked on a human still reports status "running".
func TestSummaryNeedsHuman(t *testing.T) {
	blocked := &Run{
		status:    RunRunning,
		questions: map[string]*pendingQuestion{"q1": {}},
	}
	if got := blocked.Summary()["needs_human"]; got != true {
		t.Errorf("needs_human = %v, want true", got)
	}

	idle := &Run{status: RunRunning, questions: map[string]*pendingQuestion{}}
	if got := idle.Summary()["needs_human"]; got != false {
		t.Errorf("needs_human = %v, want false", got)
	}
}
