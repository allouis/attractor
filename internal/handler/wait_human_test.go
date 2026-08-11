package handler

import (
	"testing"
	"time"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/interviewer"
)

// timeoutInterviewer records the question it was asked and always times out.
type timeoutInterviewer struct{ got interviewer.Question }

func (t *timeoutInterviewer) Ask(q interviewer.Question) (interviewer.Answer, error) {
	t.got = q
	return interviewer.Answer{Value: interviewer.AnswerTimeout}, nil
}

func buildGate(t *testing.T, gateAttrs string) *graph.Graph {
	t.Helper()
	src := `digraph d {
		gate [shape=hexagon, label="Approve?"` + gateAttrs + `]
		ship [shape=box]
		fix [shape=box]
		gate -> ship [label="[A] Approve"]
		gate -> fix [label="[F] Fix"]
	}`
	file, err := dot.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g, err := graph.Build(file)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return g
}

// A wait.human timeout with human.default_choice set selects that edge as
// if a human chose it, and passes the configured timeout to the
// interviewer (spec §6.5).
func TestWaitHumanTimeoutSelectsDefaultChoice(t *testing.T) {
	g := buildGate(t, `, human.default_choice="[F] Fix", human.timeout_seconds="2"`)
	var events []engine.Event
	iv := &timeoutInterviewer{}
	env := engine.HandlerEnv{Node: g.Nodes["gate"], Graph: g, RunID: "r", Context: engine.NewContext(),
		Emit: func(ev engine.Event) { events = append(events, ev) }}

	out := WaitHuman{Interviewer: iv}.Execute(env)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("outcome = %v, want success", out.Status)
	}
	if out.PreferredLabel != "[F] Fix" {
		t.Fatalf("preferred_label = %q, want [F] Fix", out.PreferredLabel)
	}
	if iv.got.Timeout != 2*time.Second {
		t.Fatalf("question timeout = %v, want 2s", iv.got.Timeout)
	}
	if out.ContextUpdates["human.gate.timeout"] != "true" {
		t.Fatalf("timeout not recorded in context")
	}
	var timedOut bool
	for _, ev := range events {
		if ev.Kind == engine.EventInterviewTimeout {
			timedOut = true
		}
	}
	if !timedOut {
		t.Fatalf("no InterviewTimeout event emitted (spec §9.6)")
	}
}

// noteInterviewer answers with a chosen option carrying a free-text note.
type noteInterviewer struct {
	key, label, note string
}

func (n noteInterviewer) Ask(q interviewer.Question) (interviewer.Answer, error) {
	return interviewer.Answer{
		Value:          interviewer.AnswerChoice,
		SelectedOption: &interviewer.Option{Key: n.key, Label: n.label},
		Text:           n.note,
	}, nil
}

// A wait.human answer that carries a note records it into context as
// human.note, so the revisited node's prompt can surface it as feedback.
func TestWaitHumanRecordsNote(t *testing.T) {
	g := buildGate(t, ``)
	env := engine.HandlerEnv{Node: g.Nodes["gate"], Graph: g, RunID: "r", Context: engine.NewContext(),
		Emit: func(engine.Event) {}}
	iv := noteInterviewer{key: "F", label: "[F] Fix", note: "tighten the error path"}
	out := WaitHuman{Interviewer: iv}.Execute(env)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("outcome = %v, want success", out.Status)
	}
	if out.ContextUpdates["human.note"] != "tighten the error path" {
		t.Fatalf("human.note = %q, want the answer note", out.ContextUpdates["human.note"])
	}
	if out.PreferredLabel != "[F] Fix" {
		t.Fatalf("preferred_label = %q, want [F] Fix", out.PreferredLabel)
	}
}

// A choice answer without a note records an empty human.note (the seeded
// default is preserved, not corrupted).
func TestWaitHumanNoteAbsent(t *testing.T) {
	g := buildGate(t, ``)
	env := engine.HandlerEnv{Node: g.Nodes["gate"], Graph: g, RunID: "r", Context: engine.NewContext(),
		Emit: func(engine.Event) {}}
	out := WaitHuman{Interviewer: noteInterviewer{key: "A", label: "[A] Approve"}}.Execute(env)
	if _, ok := out.ContextUpdates["human.note"]; !ok {
		t.Fatalf("human.note should be set (empty) on every answer")
	}
	if out.ContextUpdates["human.note"] != "" {
		t.Fatalf("human.note = %q, want empty", out.ContextUpdates["human.note"])
	}
}

// A timeout with no default_choice retries the gate.
func TestWaitHumanTimeoutNoDefaultRetries(t *testing.T) {
	g := buildGate(t, ``)
	env := engine.HandlerEnv{Node: g.Nodes["gate"], Graph: g, RunID: "r", Context: engine.NewContext(),
		Emit: func(engine.Event) {}}
	out := WaitHuman{Interviewer: &timeoutInterviewer{}}.Execute(env)
	if out.Status != engine.StatusRetry {
		t.Fatalf("outcome = %v, want retry", out.Status)
	}
}

// The InterviewStarted event carries the full question — options included
// (spec §9.6) — so a frontend consuming the event stream (e.g. a
// phone-home daemon) can present the choices without a side channel.
func TestWaitHumanEmitsQuestionWithOptions(t *testing.T) {
	src := `digraph d {
		gate [shape=hexagon, label="Approve?"]
		ship [shape=box]
		fix [shape=box]
		gate -> ship [label="[A] Approve"]
		gate -> fix [label="[F] Fix"]
	}`
	file, err := dot.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g, err := graph.Build(file)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	var events []engine.Event
	env := engine.HandlerEnv{
		Node:    g.Nodes["gate"],
		Graph:   g,
		RunID:   "run1",
		Emit:    func(ev engine.Event) { events = append(events, ev) },
		Context: engine.NewContext(),
	}
	h := WaitHuman{Interviewer: interviewer.AutoApprove{}}
	if out := h.Execute(env); out.Status != engine.StatusSuccess {
		t.Fatalf("outcome = %v", out.Status)
	}

	var started *engine.Event
	for i := range events {
		if events[i].Kind == engine.EventInterviewStarted {
			started = &events[i]
		}
	}
	if started == nil {
		t.Fatal("no interview_started event emitted")
	}
	if started.Question == nil {
		t.Fatal("interview_started carried no question payload (spec §9.6)")
	}
	if len(started.Question.Options) != 2 {
		t.Fatalf("question options = %d, want 2", len(started.Question.Options))
	}
	labels := map[string]bool{}
	for _, o := range started.Question.Options {
		labels[o.Label] = true
	}
	if !labels["[A] Approve"] || !labels["[F] Fix"] {
		t.Fatalf("options = %+v, want the two edge labels", started.Question.Options)
	}
}
