package server

import (
	"testing"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/interviewer"
)

// A phone-home gate answered with a note stores the note alongside the choice
// for the polling child to collect via /control (reject-with-why feedback).
func TestPhoneHomeAnswerCarriesNote(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	run := srv.registry.NewRun("digraph{}", nil, nil, tmp, nil, "", "", "", nil)

	run.Ingest(engine.Event{
		Kind:       engine.EventInterviewStarted,
		NodeID:     "plan_gate",
		QuestionID: "plan_gate-run1",
		Question: &engine.InterviewQuestion{
			Text:    "Does the plan make sense?",
			Type:    "multiple_choice",
			Stage:   "plan_gate",
			Options: []engine.InterviewOption{{Key: "A", Label: "[A] Approve plan"}, {Key: "R", Label: "[R] Revise plan"}},
		},
	})

	if err := run.SubmitAnswer("plan_gate-run1", AnswerPayload{Key: "R", Label: "[R] Revise plan", Text: "prefer a smaller first commit"}); err != nil {
		t.Fatalf("SubmitAnswer: %v", err)
	}
	got, ok := run.polledAnswers()["plan_gate-run1"]
	if !ok {
		t.Fatal("answer not stored for polling child")
	}
	if got.Key != "R" || got.Text != "prefer a smaller first commit" {
		t.Fatalf("polled answer = %+v, want key R + note", got)
	}
}

// An in-process (channel) gate answered with a note delivers the note on the
// Answer so the wait.human handler records $context.human.note.
func TestInProcessAnswerCarriesNote(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	run := srv.registry.NewRun("digraph{}", nil, nil, tmp, nil, "", "", "", nil)

	qid, ch := run.registerQuestion(interviewer.Question{ID: "q1"}, "plan_gate")
	go func() {
		_ = run.SubmitAnswer(qid, AnswerPayload{Key: "R", Label: "[R] Revise plan", Text: "add a failing test first"})
	}()
	ans := <-ch
	if ans.Value != interviewer.AnswerChoice {
		t.Fatalf("value = %v, want choice", ans.Value)
	}
	if ans.Text != "add a failing test first" {
		t.Fatalf("delivered note = %q, want carried through", ans.Text)
	}
}
