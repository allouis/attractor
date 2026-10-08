package acp

import (
	"strings"
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

// codex-acp reports a request failure (unknown model, auth) as ordinary
// message chunks followed by end_turn, so the stage "succeeded" with an
// error object as its response: a review lens produced `{"type":"error",
// ...}` and the run carried on as if it had been reviewed (run
// 60b867a00b9b, 2026-10-08). An error payload as the final message is a
// failed stage, so default_max_retries gets its chance.
func TestResultFromStopFailsOnErrorPayload(t *testing.T) {
	text := "Warning: Model metadata for `gpt-6.1-sol` not found.\n\n" +
		`{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"The 'gpt-6.1-sol' model is not supported"}}` + "\n\n"
	res := resultFromStop(acp.StopEndTurn, text)
	if res.Outcome == nil || res.Outcome.Status != engine.StatusFail {
		t.Fatalf("outcome = %+v, want fail", res.Outcome)
	}
	if want := "model is not supported"; !strings.Contains(res.Outcome.FailureReason, want) {
		t.Fatalf("failure reason %q should carry the error message", res.Outcome.FailureReason)
	}
}

// A real review that merely quotes an error object inline is still a
// response: only a message that IS the error payload fails.
func TestResultFromStopKeepsProseThatMentionsErrors(t *testing.T) {
	text := "The handler swallows `{\"type\":\"error\"}` responses at foo.go:12; that is the defect."
	res := resultFromStop(acp.StopEndTurn, text)
	if res.Outcome != nil {
		t.Fatalf("prose response must not be treated as an error payload: %+v", res.Outcome)
	}
}
