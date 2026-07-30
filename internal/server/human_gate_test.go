package server

import (
	"testing"

	"github.com/allouis/attractor/internal/engine"
)

// Ingesting an InterviewStarted event registers a pending question from the
// event payload (spec §9.6) so the run reports needs_human and the existing
// /questions surface presents it; answering it stores the answer for the
// polling child to collect via /control.
func TestPhoneHomeHumanGateRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	run := srv.registry.NewRun("digraph{}", nil, nil, tmp, nil, "", "", nil)

	run.Ingest(engine.Event{
		Kind:       engine.EventInterviewStarted,
		NodeID:     "gate",
		QuestionID: "gate-run1",
		Question: &engine.InterviewQuestion{
			Text:    "Approve?",
			Type:    "multiple_choice",
			Stage:   "gate",
			Options: []engine.InterviewOption{{Key: "A", Label: "Approve"}, {Key: "F", Label: "Fix"}},
		},
	})

	// Pending + needs_human.
	if pending := run.PendingQuestions(); len(pending) != 1 {
		t.Fatalf("pending questions = %d, want 1", len(pending))
	}
	if run.Summary()["needs_human"] != true {
		t.Fatalf("needs_human not set while gated")
	}
	// Options survived into the presented question.
	q := run.PendingQuestions()[0]
	if opts, _ := q["options"].([]map[string]any); len(opts) != 2 {
		t.Fatalf("presented options = %v, want 2", q["options"])
	}

	// Answer via the existing surface → stored for the polling child.
	if err := run.SubmitAnswer("gate-run1", AnswerPayload{Key: "A", Label: "Approve"}); err != nil {
		t.Fatalf("SubmitAnswer: %v", err)
	}
	if len(run.PendingQuestions()) != 0 {
		t.Fatalf("question still pending after answer")
	}
	ans := run.polledAnswers()
	if got, ok := ans["gate-run1"]; !ok || got.Label != "Approve" || got.Key != "A" {
		t.Fatalf("polled answer = %+v, want Approve", ans)
	}
}
