package server

import (
	"strings"
	"testing"
)

// TestGateAnswerOption: choosing a multiple-choice option builds a
// key+label payload (server AnswerPayload → AnswerChoice), web-ui-v2-spec U5.
func TestGateAnswerOption(t *testing.T) {
	got := evalUI(t, `JSON.stringify(gateAnswer({key:'A', label:'Approve'}))`)
	if got != `{"key":"A","label":"Approve"}` {
		t.Errorf("option answer payload = %s", got)
	}
}

// TestGateAnswerOptionWithNote: a note attached to a choice rides the payload
// as text (server AnswerPayload.Text → $context.human.note), especially the
// "why" on a reject.
func TestGateAnswerOptionWithNote(t *testing.T) {
	got := evalUI(t, `JSON.stringify(gateAnswer({key:'R', label:'Revise'}, null, 'split the loop first'))`)
	if got != `{"key":"R","label":"Revise","text":"split the loop first"}` {
		t.Errorf("option+note payload = %s", got)
	}
}

// TestGateAnswerOptionBlankNoteOmitted: an empty note keeps a bare key+label
// payload — a plain approval carries no text.
func TestGateAnswerOptionBlankNoteOmitted(t *testing.T) {
	got := evalUI(t, `JSON.stringify(gateAnswer({key:'A', label:'Approve'}, null, ''))`)
	if got != `{"key":"A","label":"Approve"}` {
		t.Errorf("blank-note payload should omit text, got %s", got)
	}
}

// TestGateAnswerFreeText: a run with no options takes free text, which builds
// a text-only payload (server AnswerPayload → AnswerText).
func TestGateAnswerFreeText(t *testing.T) {
	got := evalUI(t, `JSON.stringify(gateAnswer(null, 'ship it'))`)
	if got != `{"text":"ship it"}` {
		t.Errorf("free-text answer payload = %s", got)
	}
}

// TestGateAnswerLabelCarriedAsData: an option label containing quotes/markup
// rides the payload as a plain string value, never interpolated into markup —
// the reason renderDock builds the dock via DOM, not an HTML string.
func TestGateAnswerLabelCarriedAsData(t *testing.T) {
	got := evalUI(t, `JSON.stringify(gateAnswer({key:'X', label:'a"<b>'}))`)
	if !strings.Contains(got, `"label":"a\"<b>"`) {
		t.Errorf("label should be carried verbatim as a JSON value, got %s", got)
	}
}
