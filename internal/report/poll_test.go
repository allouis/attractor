package report

import (
	"testing"

	"github.com/allouis/attractor/internal/interviewer"
)

// TestControlToAnswerCarriesNote: a relayed choice answer keeps its free-text
// note (Text), so the polling child's wait.human handler records it as
// $context.human.note — the reject-with-why feedback survives the daemon→child
// control hop.
func TestControlToAnswerCarriesNote(t *testing.T) {
	a := controlToAnswer(ControlAnswer{Key: "R", Label: "[R] Revise plan", Text: "split the loop first"})
	if a.Value != interviewer.AnswerChoice {
		t.Fatalf("value = %v, want choice", a.Value)
	}
	if a.SelectedOption == nil || a.SelectedOption.Key != "R" {
		t.Fatalf("option = %+v, want key R", a.SelectedOption)
	}
	if a.Text != "split the loop first" {
		t.Fatalf("note = %q, want carried through", a.Text)
	}
}

// A choice answer with no note relays an empty note, not a dropped field.
func TestControlToAnswerNoteAbsent(t *testing.T) {
	a := controlToAnswer(ControlAnswer{Key: "A", Label: "[A] Approve plan"})
	if a.Text != "" {
		t.Fatalf("note = %q, want empty", a.Text)
	}
}
