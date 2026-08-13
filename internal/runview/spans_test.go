package runview

import (
	"testing"
	"time"

	"github.com/allouis/attractor/internal/engine"
)

func ts(sec int) time.Time {
	return time.Date(2026, 8, 13, 10, 0, sec, 0, time.UTC)
}

// D3/D4: spans are a pure fold over the event log. Identity is
// (node_id, visit, attempt); a span opens at stage_started and closes
// at the matching stage_retrying/stage_completed/stage_failed.
func TestSpans_FoldBasics(t *testing.T) {
	events := []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1, Timestamp: ts(0)},
		{Kind: engine.EventStageStarted, Seq: 2, Timestamp: ts(1), NodeID: "work", Visit: 1, Attempt: 1},
		{Kind: engine.EventStageRetrying, Seq: 3, Timestamp: ts(3), NodeID: "work", Visit: 1, Attempt: 1, Message: "429"},
		{Kind: engine.EventStageStarted, Seq: 4, Timestamp: ts(4), NodeID: "work", Visit: 1, Attempt: 2},
		{Kind: engine.EventStageCompleted, Seq: 5, Timestamp: ts(9), NodeID: "work", Visit: 1, Attempt: 2, Status: "success", Duration: 5 * time.Second},
		{Kind: engine.EventPipelineCompleted, Seq: 6, Timestamp: ts(9), Status: "success"},
	}
	spans := Spans(events)
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2: %+v", len(spans), spans)
	}
	first, second := spans[0], spans[1]
	if first.NodeID != "work" || first.Visit != 1 || first.Attempt != 1 {
		t.Fatalf("first span identity wrong: %+v", first)
	}
	if first.Outcome != "retry" || first.Detail != "429" {
		t.Fatalf("first span should close as retry with the reason: %+v", first)
	}
	if !first.EndedAt.Equal(ts(3)) {
		t.Fatalf("first span end = %v, want %v", first.EndedAt, ts(3))
	}
	if second.Attempt != 2 || second.Outcome != "success" {
		t.Fatalf("second span wrong: %+v", second)
	}
	if second.StartedAt != ts(4) || second.EndedAt != ts(9) {
		t.Fatalf("second span timestamps wrong: %+v", second)
	}
}

// A span with no closing event (the run is live or died hard) stays
// open: EndedAt zero, Outcome "running".
func TestSpans_OpenSpanIsRunning(t *testing.T) {
	events := []engine.Event{
		{Kind: engine.EventStageStarted, Seq: 1, Timestamp: ts(1), NodeID: "work", Visit: 1, Attempt: 1},
	}
	spans := Spans(events)
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Outcome != "running" || !spans[0].EndedAt.IsZero() {
		t.Fatalf("open span should be running: %+v", spans[0])
	}
}

// Usage events attribute tokens to the enclosing open span of their node.
func TestSpans_UsageAttributedToSpan(t *testing.T) {
	events := []engine.Event{
		{Kind: engine.EventStageStarted, Seq: 1, Timestamp: ts(1), NodeID: "work", Visit: 1, Attempt: 1},
		{Kind: engine.EventUsage, Seq: 2, Timestamp: ts(2), NodeID: "work", Visit: 1, Usage: &engine.Usage{InputTokens: 100, OutputTokens: 40}},
		{Kind: engine.EventStageCompleted, Seq: 3, Timestamp: ts(3), NodeID: "work", Visit: 1, Attempt: 1, Status: "success"},
	}
	spans := Spans(events)
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].InputTokens != 100 || spans[0].OutputTokens != 40 {
		t.Fatalf("usage not attributed: %+v", spans[0])
	}
}

// Events with Visit 0 (older logs, child events) fold under visit 1 so
// pre-Visit event logs still render.
func TestSpans_ZeroVisitDefaultsToOne(t *testing.T) {
	events := []engine.Event{
		{Kind: engine.EventStageStarted, Seq: 1, Timestamp: ts(1), NodeID: "work", Attempt: 1},
		{Kind: engine.EventStageCompleted, Seq: 2, Timestamp: ts(2), NodeID: "work", Attempt: 1, Status: "success"},
	}
	spans := Spans(events)
	if len(spans) != 1 || spans[0].Visit != 1 {
		t.Fatalf("zero-visit events should fold under visit 1: %+v", spans)
	}
}

// stage_failed closes its span as fail (falling back to "fail" when
// the event carries no Status).
func TestSpans_StageFailedCloses(t *testing.T) {
	events := []engine.Event{
		{Kind: engine.EventStageStarted, Seq: 1, Timestamp: ts(1), NodeID: "work", Visit: 1, Attempt: 1},
		{Kind: engine.EventStageFailed, Seq: 2, Timestamp: ts(2), NodeID: "work", Visit: 1, Attempt: 1, Message: "boom"},
	}
	spans := Spans(events)
	if len(spans) != 1 || spans[0].Outcome != "fail" || spans[0].Detail != "boom" {
		t.Fatalf("stage_failed fold wrong: %+v", spans)
	}
}

// A re-delivered event (same Seq — at-least-once delivery from a
// scraped/re-shipped log) folds once: a duplicate stage_started must
// not orphan the real span.
func TestSpans_DuplicateSeqFoldsOnce(t *testing.T) {
	started := engine.Event{Kind: engine.EventStageStarted, Seq: 1, Timestamp: ts(1), NodeID: "work", Visit: 1, Attempt: 1}
	events := []engine.Event{
		started,
		started, // duplicate delivery
		{Kind: engine.EventStageCompleted, Seq: 2, Timestamp: ts(2), NodeID: "work", Visit: 1, Attempt: 1, Status: "success"},
	}
	spans := Spans(events)
	if len(spans) != 1 || spans[0].Outcome != "success" {
		t.Fatalf("duplicate seq not deduped: %+v", spans)
	}
}

// A tool_call_update reporting failure marks the original tick red
// instead of appending a second tick.
func TestSpans_FailedToolCallUpdateMarksTick(t *testing.T) {
	events := []engine.Event{
		{Kind: engine.EventStageStarted, Seq: 1, Timestamp: ts(1), NodeID: "work", Visit: 1, Attempt: 1},
		{Kind: engine.EventStageProgress, Seq: 2, Timestamp: ts(2), NodeID: "work", Visit: 1, Detail: map[string]string{"kind": "tool_call", "status": "in_progress", "tool_call_id": "tc-1"}},
		{Kind: engine.EventStageProgress, Seq: 3, Timestamp: ts(3), NodeID: "work", Visit: 1, Detail: map[string]string{"kind": "tool_call", "status": "failed", "tool_call_id": "tc-1"}},
		{Kind: engine.EventStageCompleted, Seq: 4, Timestamp: ts(4), NodeID: "work", Visit: 1, Attempt: 1, Status: "success"},
	}
	spans := Spans(events)
	if len(spans) != 1 || len(spans[0].ToolCalls) != 1 {
		t.Fatalf("tick count wrong: %+v", spans)
	}
	if !spans[0].ToolCalls[0].Failed {
		t.Fatalf("failed update did not mark the tick: %+v", spans[0].ToolCalls)
	}
}

// Tool-call progress events tick their span (count + error flag), the
// SSSF-style tick marks the waterfall renders inside a span bar.
func TestSpans_ToolCallTicks(t *testing.T) {
	events := []engine.Event{
		{Kind: engine.EventStageStarted, Seq: 1, Timestamp: ts(1), NodeID: "work", Visit: 1, Attempt: 1},
		{Kind: engine.EventStageProgress, Seq: 2, Timestamp: ts(2), NodeID: "work", Visit: 1, Detail: map[string]string{"kind": "tool_call", "status": "in_progress", "tool_call_id": "tc-1"}},
		{Kind: engine.EventStageProgress, Seq: 3, Timestamp: ts(3), NodeID: "work", Visit: 1, Detail: map[string]string{"kind": "tool_call", "status": "completed", "tool_call_id": "tc-1"}},
		{Kind: engine.EventStageCompleted, Seq: 4, Timestamp: ts(4), NodeID: "work", Visit: 1, Attempt: 1, Status: "success"},
	}
	spans := Spans(events)
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if len(spans[0].ToolCalls) != 1 {
		t.Fatalf("tool call ticks wrong: %+v", spans[0].ToolCalls)
	}
	tc := spans[0].ToolCalls[0]
	if !tc.At.Equal(ts(2)) || tc.Failed {
		t.Fatalf("tick wrong: %+v", tc)
	}
}
