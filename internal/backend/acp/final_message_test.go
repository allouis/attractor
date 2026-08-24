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

	ts.arm() // the prompt turn has started; no session replay in this test

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
	ts.arm()
	ts.onUpdate(acp.SessionUpdate{Kind: "agent_message_chunk", Text: "All done."})
	ts.onUpdate(acp.SessionUpdate{Kind: "tool_call", ToolCall: &acp.ToolCallUpdate{ID: "t1"}})

	if got := ts.text(); got != "All done." {
		t.Fatalf("text() = %q, want %q", got, "All done.")
	}
}

// session/load replays the whole prior conversation as message chunks before
// the prompt turn starts. Un-armed chunks must not enter the capture: a
// replayed transcript once got captured wholesale when the live turn made no
// tool calls (nothing ever reset the boundary), so a plan review was handed
// the entire conversation instead of the newest plan.
func TestTurnStateIgnoresReplayBeforeArm(t *testing.T) {
	ts := &turnState{env: engine.HandlerEnv{Node: &graph.Node{ID: "n", Attrs: map[string]string{}}}}
	chunk := func(s string) {
		ts.onUpdate(acp.SessionUpdate{Kind: "agent_message_chunk", Text: s})
	}

	// Replay from session/load: prior turns, no tool_call events, not armed.
	chunk("old plan v1. ")
	chunk("old plan v2. ")

	ts.arm()

	// The live turn emits only text — no tool calls to reset any boundary.
	chunk("The new ")
	chunk("plan.")

	if got := ts.text(); got != "The new plan." {
		t.Fatalf("text() = %q, want the post-arm message only", got)
	}
}
