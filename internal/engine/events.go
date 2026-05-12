package engine

import "time"

// Event is the supertype of all engine-emitted events (spec §9.6). It
// carries the event Kind plus a small payload of common fields; richer
// data lives in the typed sub-fields.
type Event struct {
	Kind        EventKind         `json:"kind"`
	Timestamp   time.Time         `json:"ts"`
	RunID       string            `json:"run_id,omitempty"`
	NodeID      string            `json:"node_id,omitempty"`
	Status      string            `json:"status,omitempty"`
	Message     string            `json:"message,omitempty"`
	Attempt     int               `json:"attempt,omitempty"`
	DelayMs     int               `json:"delay_ms,omitempty"`
	Duration    time.Duration     `json:"duration_ns,omitempty"`
	Detail      map[string]string `json:"detail,omitempty"`
	QuestionID  string            `json:"question_id,omitempty"`
}

// EventKind enumerates pipeline-engine lifecycle events.
type EventKind string

const (
	EventPipelineStarted   EventKind = "pipeline_started"
	EventPipelineCompleted EventKind = "pipeline_completed"
	EventPipelineFailed    EventKind = "pipeline_failed"
	EventStageStarted      EventKind = "stage_started"
	EventStageCompleted    EventKind = "stage_completed"
	EventStageFailed       EventKind = "stage_failed"
	EventStageRetrying     EventKind = "stage_retrying"
	EventStageProgress     EventKind = "stage_progress"
	EventCheckpointSaved   EventKind = "checkpoint_saved"
	EventInterviewStarted  EventKind = "interview_started"
	EventInterviewAnswered EventKind = "interview_answered"
)
