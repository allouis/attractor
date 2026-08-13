package engine

import (
	"testing"
)

// failOnceHandler fails its first execution and succeeds afterwards,
// driving a two-visit loop through a retry edge.
type failOnceHandler struct{ calls *int }

func (h failOnceHandler) Execute(env HandlerEnv) Outcome {
	*h.calls++
	if *h.calls == 1 {
		return Outcome{Status: StatusFail, FailureReason: "first visit fails"}
	}
	return Outcome{Status: StatusSuccess}
}

// D3: every node execution attempt is a span identified by
// (node_id, visit, attempt). The engine stamps the current visit number
// on every event it emits for a node, so spans are a pure fold over the
// event log — no inference by counting stage_started events.
func TestVisit_StampedOnStageEvents(t *testing.T) {
	g := buildGraph(t, `digraph t {
		max_node_visits=5
		max_repeated_failures=0
		start [shape=Mdiamond]
		work [type="flaky"]
		done [shape=Msquare]
		start -> work
		work -> work [condition="outcome=fail"]
		work -> done [condition="outcome=success"]
	}`)
	calls := 0
	reg := NewRegistry()
	reg.Register("start", okHandler{})
	reg.Register("flaky", failOnceHandler{calls: &calls})
	eng := New(Config{Registry: reg, LogsRoot: t.TempDir()})

	var events []Event
	done := make(chan struct{})
	go func() {
		for ev := range eng.Events() {
			events = append(events, ev)
		}
		close(done)
	}()
	if _, err := eng.Run(&PreparedGraph{Graph: g}); err != nil {
		t.Fatalf("run: %v", err)
	}
	<-done

	visits := map[int]bool{}
	for _, ev := range events {
		if ev.NodeID != "work" {
			continue
		}
		switch ev.Kind {
		case EventStageStarted, EventStageCompleted, EventStageFailed:
			if ev.Visit == 0 {
				t.Fatalf("%s event for work has no Visit stamp: %+v", ev.Kind, ev)
			}
			visits[ev.Visit] = true
		}
	}
	if !visits[1] || !visits[2] {
		t.Fatalf("expected events for visits 1 and 2, got %v", visits)
	}
}
