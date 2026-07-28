package client

import (
	"encoding/json"
	"time"
)

// RunSummary is one entry in the daemon's run list (GET /pipelines) and
// the shape of GET /pipelines/{id}. It mirrors the server's Summary()
// JSON; the TUI builds its run list from these.
type RunSummary struct {
	ID            string    `json:"id"`
	Status        string    `json:"status"`
	GraphName     string    `json:"graph_name,omitempty"`
	Cwd           string    `json:"cwd,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	CompletedAt   time.Time `json:"completed_at,omitempty"`
	Outcome       string    `json:"outcome,omitempty"`
	FailureReason string    `json:"failure_reason,omitempty"`
	Events        int       `json:"events,omitempty"`
	LogsRoot      string    `json:"logs_root,omitempty"`
	Tokens        *Usage    `json:"tokens,omitempty"`
	ItemRef       string    `json:"item_ref,omitempty"`
}

// Usage is per-run token accounting rolled up on the terminal event.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ItemRef is the external Item a run is dispatched for (items-spec I1),
// used in the submit-request body. It mirrors items.ItemRef so this
// package stays dependency-free. On the response side a run's item link
// is the opaque tag string (RunSummary.ItemRef).
type ItemRef struct {
	Source     string `json:"source"`
	Type       string `json:"type"`
	ExternalID string `json:"external_id"`
}

// Event is one engine event as delivered over the SSE stream. It mirrors
// engine.Event's JSON envelope; the client keeps its own copy so the
// package stays dependency-free and the wire shape is the contract. The
// Seq field is populated once the server stamps sequence numbers (T3).
type Event struct {
	Kind       string            `json:"kind"`
	Timestamp  time.Time         `json:"ts"`
	RunID      string            `json:"run_id,omitempty"`
	NodeID     string            `json:"node_id,omitempty"`
	Status     string            `json:"status,omitempty"`
	Message    string            `json:"message,omitempty"`
	Attempt    int               `json:"attempt,omitempty"`
	DelayMs    int               `json:"delay_ms,omitempty"`
	Duration   int64             `json:"duration_ns,omitempty"`
	Detail     map[string]string `json:"detail,omitempty"`
	QuestionID string            `json:"question_id,omitempty"`
	Usage      *Usage            `json:"usage,omitempty"`
	Seq        int64             `json:"seq,omitempty"`
}

// Question is an open human gate awaiting an answer.
type Question struct {
	ID      string   `json:"id"`
	NodeID  string   `json:"node_id,omitempty"`
	Text    string   `json:"text"`
	Options []Option `json:"options"`
}

// Option is one selectable answer for a Question.
type Option struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// StageDetail is a single stage's inline output (tui-spec T2), returned by
// GET /pipelines/{id}/stages/{node}. Status mirrors the stage's status.json
// outcome ("" while still running). ToolCalls holds each tool_calls/*.json
// payload verbatim so the client stays agnostic about the hook wire shape.
type StageDetail struct {
	Status    string            `json:"status"`
	Prompt    string            `json:"prompt"`
	Response  string            `json:"response"`
	ToolCalls []json.RawMessage `json:"tool_calls"`
}

// SubmitRequest is the body of POST /pipelines: the DOT source plus the
// optional variable bindings and working directory (service-spec §2).
type SubmitRequest struct {
	Dot     string            `json:"dot"`
	Cwd     string            `json:"cwd,omitempty"`
	Vars    map[string]string `json:"vars,omitempty"`
	ItemRef *ItemRef          `json:"item_ref,omitempty"`
}
