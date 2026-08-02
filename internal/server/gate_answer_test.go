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
