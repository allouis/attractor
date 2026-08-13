package runview

import (
	"time"

	"github.com/allouis/attractor/internal/engine"
)

// RunDoc is the enriched pipeline document (local-first plan D4): the
// spec §9.5 GET /pipelines/{id} payload absorbing summary, spans,
// active nodes, and pending questions. It is a pure fold over
// (run.json, events) — reconstructible by any client from
// /events?since=0.
type RunDoc struct {
	RunID     string    `json:"run_id"`
	GraphName string    `json:"graph_name,omitempty"`
	Goal      string    `json:"goal,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	// Status: running | completed | failed.
	Status        string    `json:"status"`
	FailureReason string    `json:"failure_reason,omitempty"`
	EndedAt       time.Time `json:"ended_at,omitzero"`

	Spans       []Span   `json:"spans"`
	ActiveNodes []string `json:"active_nodes,omitempty"`

	PendingQuestions []PendingQuestion `json:"pending_questions,omitempty"`

	Usage engine.Usage `json:"usage"`
	// LastSeq is the highest event sequence folded in — the client's
	// next ?since= cursor.
	LastSeq int64 `json:"last_seq"`
}

// PendingQuestion is an unanswered human gate.
type PendingQuestion struct {
	ID       string                    `json:"id"`
	NodeID   string                    `json:"node_id"`
	AskedAt  time.Time                 `json:"asked_at"`
	Message  string                    `json:"message,omitempty"`
	Question *engine.InterviewQuestion `json:"question,omitempty"`
}

// Document folds a run's manifest and event log into the enriched
// pipeline document.
func Document(m engine.Manifest, events []engine.Event) RunDoc {
	doc := RunDoc{
		RunID:     m.RunID,
		GraphName: m.GraphName,
		Goal:      m.Goal,
		StartedAt: m.StartedAt,
		Status:    "running",
		Spans:     Spans(events),
	}
	pending := []PendingQuestion{}
	for _, ev := range events {
		if ev.Seq > doc.LastSeq {
			doc.LastSeq = ev.Seq
		}
		switch ev.Kind {
		case engine.EventPipelineCompleted:
			doc.Status = "completed"
			doc.EndedAt = ev.Timestamp
			if ev.Usage != nil {
				doc.Usage = *ev.Usage
			}
		case engine.EventPipelineFailed:
			doc.Status = "failed"
			doc.FailureReason = ev.Message
			doc.EndedAt = ev.Timestamp
			if ev.Usage != nil {
				doc.Usage = *ev.Usage
			}
		case engine.EventUsage:
			if ev.Usage != nil && doc.Status == "running" {
				doc.Usage.Add(*ev.Usage)
			}
		case engine.EventInterviewStarted:
			pending = append(pending, PendingQuestion{
				ID:       ev.QuestionID,
				NodeID:   ev.NodeID,
				AskedAt:  ev.Timestamp,
				Message:  ev.Message,
				Question: ev.Question,
			})
		case engine.EventInterviewAnswered, engine.EventInterviewTimeout:
			for i := range pending {
				if pending[i].ID == ev.QuestionID {
					pending = append(pending[:i], pending[i+1:]...)
					break
				}
			}
		}
	}
	if len(pending) > 0 {
		doc.PendingQuestions = pending
	}
	// A hard-terminated run (LG1/LG2 abort, crash) can leave spans with
	// no closing event; reporting them active alongside a terminal
	// status would contradict itself, so active nodes exist only while
	// the run does.
	if doc.Status == "running" {
		for _, s := range doc.Spans {
			if s.Outcome == OutcomeRunning {
				doc.ActiveNodes = append(doc.ActiveNodes, s.NodeID)
			}
		}
	}
	return doc
}
