package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/engine"
)

// childDetail tags a forwarded child engine event the way manager_loop does
// (handler/manager_loop.go consumeChildEvents), so the SSE handler can tell a
// nested review's terminal from the parent run's own.
func childDetail() map[string]string { return map[string]string{"source": "child"} }

// paintFromFrames mirrors the frontend graph painter (index.html HANDLERS):
// each stage event repaints its node, so the final map is what the graph shows.
func paintFromFrames(evs []engine.Event) map[string]string {
	state := map[string]string{}
	for _, ev := range evs {
		switch ev.Kind {
		case engine.EventStageStarted, engine.EventStageRetrying:
			state[ev.NodeID] = "running"
		case engine.EventStageCompleted:
			state[ev.NodeID] = "completed"
		case engine.EventStageFailed:
			state[ev.NodeID] = "failed"
		}
	}
	return state
}

// TestStreamEventsDeliversLoopedNodePastChildTerminal is the T9a acceptance for
// looped nodes: a re-entered node (review_loop / a fix-loop stage) runs a nested
// child pipeline that fails on the first visit and succeeds on the second. That
// child forwards its own terminal pipeline_failed onto the parent stream. The
// SSE handler must NOT treat a forwarded child terminal as the run's terminal —
// otherwise it closes the stream at the child failure and the looped node's
// later events (its retry + completion) never reach the client, so it stays
// "running" forever. Only the parent's own terminal ends the stream.
func TestStreamEventsDeliversLoopedNodePastChildTerminal(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	logsRoot := filepath.Join(tmp, "r1")
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Mirrors run 8f33e2e1c460a78c's shape: loop node visited twice, a child
	// pipeline failing then succeeding interleaved between the visits.
	evs := []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1},
		{Kind: engine.EventStageStarted, NodeID: "loop", Seq: 2},
		{Kind: engine.EventPipelineStarted, Seq: 3, Detail: childDetail()},
		{Kind: engine.EventPipelineFailed, Seq: 4, Detail: childDetail()},
		{Kind: engine.EventStageFailed, NodeID: "loop", Seq: 5},
		{Kind: engine.EventStageStarted, NodeID: "loop", Seq: 6},
		{Kind: engine.EventPipelineStarted, Seq: 7, Detail: childDetail()},
		{Kind: engine.EventPipelineCompleted, Seq: 8, Detail: childDetail()},
		{Kind: engine.EventStageCompleted, NodeID: "loop", Seq: 9},
		{Kind: engine.EventPipelineCompleted, Seq: 10},
	}
	f, err := os.Create(filepath.Join(logsRoot, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		line, _ := json.Marshal(ev)
		f.Write(append(line, '\n'))
	}
	f.Close()

	srv.registry.mu.Lock()
	srv.registry.runs["r1"] = &Run{
		ID:          "r1",
		logsRoot:    logsRoot,
		status:      RunCompleted,
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
	}
	srv.registry.mu.Unlock()

	resp, err := http.Get(srv.URL() + "/pipelines/r1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var got []engine.Event
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		payload, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		var ev engine.Event
		if err := json.Unmarshal([]byte(strings.TrimSpace(payload)), &ev); err != nil {
			t.Fatalf("bad SSE frame: %v", err)
		}
		got = append(got, ev)
	}

	if len(got) != len(evs) {
		t.Fatalf("delivered %d frames, want the full %d (stream cut at the child terminal?)", len(got), len(evs))
	}
	if last := got[len(got)-1]; last.Kind != engine.EventPipelineCompleted || last.Seq != 10 {
		t.Fatalf("last frame = %q seq %d, want the parent pipeline_completed seq 10", last.Kind, last.Seq)
	}
	if st := paintFromFrames(got)["loop"]; st != "completed" {
		t.Errorf("looped node = %q, want completed", st)
	}
}
