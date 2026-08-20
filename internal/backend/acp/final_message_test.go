package acp

import (
	"testing"

	acp "github.com/allouis/attractor/internal/acp"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
)

// The response contract is "the agent's final message", not a transcript:
// interim think-aloud text emitted between tool calls must not leak into
// ResponseText (it pollutes output_key captures downstream).
func TestTurnStateTextIsFinalMessageNotTranscript(t *testing.T) {
	ts := &turnState{env: engine.HandlerEnv{Node: &graph.Node{ID: "n", Attrs: map[string]string{}}}}
	chunk := func(s string) {
		ts.onUpdate(acp.SessionUpdate{Kind: "agent_message_chunk", Text: s})
	}
	tool := func() {
		ts.onUpdate(acp.SessionUpdate{Kind: "tool_call", ToolCall: &acp.ToolCallUpdate{ID: "t1"}})
	}

	chunk("Let me read the repo first.")
	tool()
	chunk("Good, that's enough context.")
	tool()
	chunk("The final ")
	chunk("report.")

	if got := ts.text(); got != "The final report." {
		t.Fatalf("text() = %q, want the final message only", got)
	}
}

// Text emitted before tool calls, with no text after, is still the last
// thing the agent said — it must survive as the response.
func TestTurnStateTextKeepsLastTextWhenToolCallsTrail(t *testing.T) {
	ts := &turnState{env: engine.HandlerEnv{Node: &graph.Node{ID: "n", Attrs: map[string]string{}}}}
	ts.onUpdate(acp.SessionUpdate{Kind: "agent_message_chunk", Text: "All done."})
	ts.onUpdate(acp.SessionUpdate{Kind: "tool_call", ToolCall: &acp.ToolCallUpdate{ID: "t1"}})

	if got := ts.text(); got != "All done." {
		t.Fatalf("text() = %q, want %q", got, "All done.")
	}
}
