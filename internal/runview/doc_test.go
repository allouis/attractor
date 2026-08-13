package runview

import (
	"testing"

	"github.com/allouis/attractor/internal/engine"
)

// D4: the enriched pipeline document is a fold over
// (run.json, events); it carries summary, spans, active nodes, and
// pending questions.
func TestDocument_SummaryAndActive(t *testing.T) {
	m := engine.Manifest{RunID: "r1", GraphName: "g", Goal: "fix it", StartedAt: ts(0)}
	events := []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1, Timestamp: ts(0)},
		{Kind: engine.EventStageStarted, Seq: 2, Timestamp: ts(1), NodeID: "plan", Visit: 1, Attempt: 1},
		{Kind: engine.EventStageCompleted, Seq: 3, Timestamp: ts(2), NodeID: "plan", Visit: 1, Attempt: 1, Status: "success"},
		{Kind: engine.EventStageStarted, Seq: 4, Timestamp: ts(3), NodeID: "impl", Visit: 1, Attempt: 1},
		{Kind: engine.EventUsage, Seq: 5, Timestamp: ts(4), NodeID: "impl", Visit: 1, Usage: &engine.Usage{InputTokens: 10, OutputTokens: 5}},
	}
	doc := Document(m, events)
	if doc.RunID != "r1" || doc.Goal != "fix it" {
		t.Fatalf("summary wrong: %+v", doc)
	}
	if doc.Status != "running" {
		t.Fatalf("status = %q, want running (no terminal event)", doc.Status)
	}
	if len(doc.Spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(doc.Spans))
	}
	if len(doc.ActiveNodes) != 1 || doc.ActiveNodes[0] != "impl" {
		t.Fatalf("active nodes wrong: %v", doc.ActiveNodes)
	}
	if doc.Usage.InputTokens != 10 || doc.Usage.OutputTokens != 5 {
		t.Fatalf("usage rollup wrong: %+v", doc.Usage)
	}
	if doc.LastSeq != 5 {
		t.Fatalf("last seq = %d, want 5", doc.LastSeq)
	}
}

func TestDocument_TerminalStatus(t *testing.T) {
	m := engine.Manifest{RunID: "r1"}
	events := []engine.Event{
		{Kind: engine.EventPipelineFailed, Seq: 1, Timestamp: ts(1), Message: "boom"},
	}
	doc := Document(m, events)
	if doc.Status != "failed" || doc.FailureReason != "boom" {
		t.Fatalf("terminal fold wrong: %+v", doc)
	}
}

// A question opened by interview_started with no matching
// interview_answered stays pending; an answered one drops out.
func TestDocument_PendingQuestions(t *testing.T) {
	m := engine.Manifest{RunID: "r1"}
	q := &engine.InterviewQuestion{Text: "ship it?", Options: []engine.InterviewOption{{Key: "S", Label: "Ship"}}}
	events := []engine.Event{
		{Kind: engine.EventInterviewStarted, Seq: 1, Timestamp: ts(1), NodeID: "gate", QuestionID: "q1", Message: "ship it?", Question: q},
		{Kind: engine.EventInterviewStarted, Seq: 2, Timestamp: ts(2), NodeID: "gate2", QuestionID: "q2", Message: "other?", Question: q},
		{Kind: engine.EventInterviewAnswered, Seq: 3, Timestamp: ts(3), NodeID: "gate2", QuestionID: "q2", Message: "yes"},
	}
	doc := Document(m, events)
	if len(doc.PendingQuestions) != 1 || doc.PendingQuestions[0].ID != "q1" {
		t.Fatalf("pending questions wrong: %+v", doc.PendingQuestions)
	}
	if doc.PendingQuestions[0].Question == nil || doc.PendingQuestions[0].Question.Text != "ship it?" {
		t.Fatalf("question payload missing: %+v", doc.PendingQuestions[0])
	}
}
