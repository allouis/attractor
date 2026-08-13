package engine

import "time"

// Event is the supertype of all engine-emitted events (spec §9.6). It
// carries the event Kind plus a small payload of common fields; richer
// data lives in the typed sub-fields.
type Event struct {
	Kind      EventKind `json:"kind"`
	Timestamp time.Time `json:"ts"`
	RunID     string    `json:"run_id,omitempty"`
	NodeID    string    `json:"node_id,omitempty"`
	Status    string    `json:"status,omitempty"`
	Message   string    `json:"message,omitempty"`
	Attempt   int       `json:"attempt,omitempty"`
	// Visit is the 1-based count of engine visits to NodeID within this
	// run incarnation (D3): span identity is (node_id, visit, attempt).
	// Stamped by emit from the engine's visit bookkeeping; 0 on events
	// with no node.
	Visit      int               `json:"visit,omitempty"`
	DelayMs    int               `json:"delay_ms,omitempty"`
	Duration   time.Duration     `json:"duration_ns,omitempty"`
	Detail     map[string]string `json:"detail,omitempty"`
	QuestionID string            `json:"question_id,omitempty"`
	Usage      *Usage            `json:"usage,omitempty"`
	// Question carries the full human-gate question on an
	// EventInterviewStarted (spec §9.6 InterviewStarted(question)), so a
	// frontend consuming the event stream — including a phone-home daemon —
	// can present the choices without a side channel.
	Question *InterviewQuestion `json:"question,omitempty"`
	// Seq is a monotonic per-run sequence number stamped by emit. It lets
	// SSE consumers resume with ?since=<seq> without duplicating events (T3).
	Seq int64 `json:"seq,omitempty"`
}

// Usage carries token accounting: per-stage on an EventUsage event, or
// the per-run rollup on the terminal pipeline event (service-spec §6).
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Add accumulates another Usage into u.
func (u *Usage) Add(other Usage) {
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
}

// InterviewQuestion is the self-contained question an EventInterviewStarted
// carries (spec §9.6). It mirrors the interviewer Question's presentable
// fields without the engine importing the interviewer package.
type InterviewQuestion struct {
	Text    string            `json:"text"`
	Type    string            `json:"type,omitempty"`
	Stage   string            `json:"stage,omitempty"`
	Options []InterviewOption `json:"options,omitempty"`
}

// InterviewOption is one multiple-choice option (key + display label).
type InterviewOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
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
	EventInterviewTimeout  EventKind = "interview_timeout"
	EventUsage             EventKind = "usage"
)
