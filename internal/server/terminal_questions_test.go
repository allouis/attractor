package server

import (
	"testing"

	"github.com/allouis/attractor/internal/engine"
)

// TestCancelClearsQuestions: cancelling a running run blocked on a wait.human
// gate drops its pending questions, so every /questions consumer — the fleet's
// needs_human flag, the run-detail dock and its loud banner — agrees the run is
// no longer waiting on anyone (web-ui-v2-spec U5). Without this a cancelled run
// keeps nagging in the detail view even though the fleet already dropped it.
func TestCancelClearsQuestions(t *testing.T) {
	run := &Run{status: RunRunning, questions: map[string]*pendingQuestion{"q1": {}}}
	run.Cancel()
	if got := run.PendingQuestions(); len(got) != 0 {
		t.Errorf("cancelled run still lists %d pending questions", len(got))
	}
	if got := run.Summary()["needs_human"]; got != false {
		t.Errorf("cancelled run still reports needs_human = %v", got)
	}
}

// TestFailCrashedClearsQuestions: a run whose driving process died mid-gate
// stops presenting the answer surface too — a crashed run cannot be unblocked.
func TestFailCrashedClearsQuestions(t *testing.T) {
	run := &Run{
		status:      RunRunning,
		questions:   map[string]*pendingQuestion{"q1": {}},
		subscribers: map[chan engine.Event]struct{}{},
	}
	run.failCrashed("driver exited")
	if got := run.PendingQuestions(); len(got) != 0 {
		t.Errorf("crashed run still lists %d pending questions", len(got))
	}
	if got := run.Summary()["needs_human"]; got != false {
		t.Errorf("crashed run still reports needs_human = %v", got)
	}
}

// TestFinishFromEventClearsQuestions: a phone-home run that reports a terminal
// pipeline event likewise drops any still-open question.
func TestFinishFromEventClearsQuestions(t *testing.T) {
	run := &Run{
		status:      RunRunning,
		questions:   map[string]*pendingQuestion{"q1": {}},
		subscribers: map[chan engine.Event]struct{}{},
	}
	run.finishFromEvent(engine.Event{Kind: engine.EventPipelineCompleted, Status: "success"})
	if got := run.PendingQuestions(); len(got) != 0 {
		t.Errorf("finished run still lists %d pending questions", len(got))
	}
	if got := run.Summary()["needs_human"]; got != false {
		t.Errorf("finished run still reports needs_human = %v", got)
	}
}
