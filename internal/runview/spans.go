// Package runview derives presentable run state from a run's own
// artifacts (events.jsonl, run.json, the graph). Everything here is a
// pure fold over those inputs — no extra storage, per local-first plan
// D3/D4: any client could reconstruct the same document from
// /events?since=0.
package runview

import (
	"time"

	"github.com/allouis/attractor/internal/engine"
)

// Span is one node execution attempt, identified by
// (node_id, visit, attempt). It opens at stage_started and closes at
// the matching stage_retrying / stage_completed / stage_failed.
type Span struct {
	NodeID    string    `json:"node_id"`
	Visit     int       `json:"visit"`
	Attempt   int       `json:"attempt"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	// Outcome: OutcomeRunning or an engine status string (success |
	// partial_success | retry | fail | skipped). OutcomeRunning means no
	// closing event yet.
	Outcome string `json:"outcome"`
	// Detail carries the closing event's message (retry reason, failure
	// reason) when there is one.
	Detail       string     `json:"detail,omitempty"`
	InputTokens  int        `json:"input_tokens,omitempty"`
	OutputTokens int        `json:"output_tokens,omitempty"`
	ToolCalls    []ToolTick `json:"tool_calls,omitempty"`
	// Node metadata (D5: spans are self-describing so lane grouping is
	// purely a frontend groupBy). Filled from the graph when available.
	Type     string `json:"type,omitempty"`
	Class    string `json:"class,omitempty"`
	Model    string `json:"model,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
}

// NodeMeta is the graph-derived metadata attached to a node's spans.
type NodeMeta struct {
	Type     string
	Class    string
	Model    string
	ThreadID string
}

// GraphMeta extracts per-node metadata from graph node attributes.
// Kept as a plain map so runview does not depend on the graph package.
func AttachMeta(spans []Span, meta map[string]NodeMeta) {
	for i := range spans {
		if m, ok := meta[spans[i].NodeID]; ok {
			spans[i].Type = m.Type
			spans[i].Class = m.Class
			spans[i].Model = m.Model
			spans[i].ThreadID = m.ThreadID
		}
	}
}

// ToolTick is one tool call observed inside a span — the waterfall
// renders these as tick marks on the span bar (red when Failed).
type ToolTick struct {
	At     time.Time `json:"at"`
	Title  string    `json:"title,omitempty"`
	Failed bool      `json:"failed,omitempty"`
}

// OutcomeRunning marks a span with no closing event yet.
const OutcomeRunning = "running"

// Spans folds an event log into execution spans, ordered by open time
// (event order). Events with Visit 0 (older logs, forwarded child
// events) fold under visit 1. Events whose Seq repeats one already
// folded are skipped: the engine's own log is exactly-once, but a
// hub-scraped or re-shipped log may deliver at least once, and a
// re-delivered stage_started would otherwise orphan the real span.
func Spans(events []engine.Event) []Span {
	var out []Span
	// open maps node id -> index into out of that node's open span.
	// Span identity includes visit+attempt, but the engine executes one
	// attempt of one node at a time per node id, so node id suffices as
	// the open-span key (parallel branches have distinct node ids).
	open := map[string]int{}
	// tickIdx maps node id -> (tool_call_id -> index into that span's
	// ToolCalls) so tool_call_update events mark the original tick
	// failed instead of appending a duplicate.
	tickIdx := map[string]map[string]int{}
	seen := map[int64]bool{}

	for _, ev := range events {
		if ev.Seq != 0 {
			if seen[ev.Seq] {
				continue
			}
			seen[ev.Seq] = true
		}
		if ev.NodeID == "" {
			continue
		}
		visit := ev.Visit
		if visit == 0 {
			visit = 1
		}
		switch ev.Kind {
		case engine.EventStageStarted:
			attempt := ev.Attempt
			if attempt == 0 {
				attempt = 1
			}
			open[ev.NodeID] = len(out)
			tickIdx[ev.NodeID] = map[string]int{}
			out = append(out, Span{
				NodeID:    ev.NodeID,
				Visit:     visit,
				Attempt:   attempt,
				StartedAt: ev.Timestamp,
				Outcome:   OutcomeRunning,
			})
		case engine.EventStageRetrying:
			if i, ok := open[ev.NodeID]; ok {
				out[i].EndedAt = ev.Timestamp
				out[i].Outcome = "retry"
				out[i].Detail = ev.Message
				delete(open, ev.NodeID)
			}
		case engine.EventStageCompleted, engine.EventStageFailed:
			if i, ok := open[ev.NodeID]; ok {
				out[i].EndedAt = ev.Timestamp
				out[i].Outcome = ev.Status
				if out[i].Outcome == "" {
					if ev.Kind == engine.EventStageFailed {
						out[i].Outcome = "fail"
					} else {
						out[i].Outcome = "success"
					}
				}
				out[i].Detail = ev.Message
				delete(open, ev.NodeID)
			}
		case engine.EventUsage:
			if i, ok := open[ev.NodeID]; ok && ev.Usage != nil {
				out[i].InputTokens += ev.Usage.InputTokens
				out[i].OutputTokens += ev.Usage.OutputTokens
			}
		case engine.EventStageProgress:
			i, ok := open[ev.NodeID]
			if !ok || ev.Detail == nil || ev.Detail["kind"] != "tool_call" {
				continue
			}
			id := ev.Detail["tool_call_id"]
			ticks := tickIdx[ev.NodeID]
			if j, seen := ticks[id]; seen && id != "" {
				// An update to a known call: record failure, keep the tick.
				if ev.Detail["status"] == "failed" {
					out[i].ToolCalls[j].Failed = true
				}
				continue
			}
			if id != "" {
				ticks[id] = len(out[i].ToolCalls)
			}
			out[i].ToolCalls = append(out[i].ToolCalls, ToolTick{
				At:     ev.Timestamp,
				Title:  ev.Message,
				Failed: ev.Detail["status"] == "failed",
			})
		}
	}
	return out
}
