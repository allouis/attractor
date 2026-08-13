package runserver

import (
	"testing"
	"time"

	"github.com/allouis/attractor/internal/interviewer"
)

// Gate is the interviewer behind the single-run server's /answer
// endpoint: Ask blocks the pipeline until Answer resolves the question.
func TestGate_AnswerResolvesAsk(t *testing.T) {
	g := NewGate()
	done := make(chan interviewer.Answer, 1)
	go func() {
		a, err := g.Ask(interviewer.Question{
			ID:   "q1",
			Type: interviewer.QuestionMultipleChoice,
			Options: []interviewer.Option{
				{Key: "S", Label: "Ship"},
				{Key: "C", Label: "Request changes"},
			},
		})
		if err != nil {
			t.Errorf("ask: %v", err)
		}
		done <- a
	}()

	// Wait until the question is registered, then answer it.
	deadline := time.After(2 * time.Second)
	for {
		if err := g.Answer("q1", "C", "needs a test"); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Answer never found the pending question")
		case <-time.After(5 * time.Millisecond):
		}
	}
	a := <-done
	if a.SelectedOption == nil || a.SelectedOption.Key != "C" {
		t.Fatalf("answer wrong: %+v", a)
	}
	if a.Text != "needs a test" {
		t.Fatalf("note lost: %+v", a)
	}
}

func TestGate_AnswerByLabel(t *testing.T) {
	g := NewGate()
	done := make(chan interviewer.Answer, 1)
	go func() {
		a, _ := g.Ask(interviewer.Question{ID: "q2", Type: interviewer.QuestionMultipleChoice,
			Options: []interviewer.Option{{Key: "S", Label: "Ship"}}})
		done <- a
	}()
	for g.Answer("q2", "Ship", "") != nil {
		time.Sleep(5 * time.Millisecond)
	}
	if a := <-done; a.SelectedOption == nil || a.SelectedOption.Key != "S" {
		t.Fatalf("label answer wrong: %+v", a)
	}
}

func TestGate_UnknownQuestionErrors(t *testing.T) {
	g := NewGate()
	if err := g.Answer("nope", "S", ""); err == nil {
		t.Fatal("want error for unknown question")
	}
}

// A question with a timeout falls back to AnswerTimeout so
// human.default_choice still works when nobody answers via HTTP.
func TestGate_Timeout(t *testing.T) {
	g := NewGate()
	a, err := g.Ask(interviewer.Question{ID: "q3", Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if a.Value != interviewer.AnswerTimeout {
		t.Fatalf("want timeout answer, got %+v", a)
	}
}
