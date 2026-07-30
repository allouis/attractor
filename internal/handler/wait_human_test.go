package handler

import (
	"testing"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/interviewer"
)

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
