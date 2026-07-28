package server

import (
	"sync"
	"testing"

	"github.com/allouis/attractor/internal/engine"
)

// TestFinishedRunReplaysTerminalEventOver128 checks that a finished run with
// more than 128 buffered events replays its entire history — including the
// terminal pipeline_completed event the UI needs to stop reconnecting.
func TestFinishedRunReplaysTerminalEventOver128(t *testing.T) {
	r := &Run{
		status:      RunCompleted,
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
	}
	const n = 200
	for i := 0; i < n; i++ {
		r.history = append(r.history, engine.Event{Kind: engine.EventStageProgress})
	}
	r.history = append(r.history, engine.Event{Kind: engine.EventPipelineCompleted})

	ch := r.Subscribe(0)
	var got []engine.Event
	for ev := range ch {
		got = append(got, ev)
	}
	if len(got) != n+1 {
		t.Fatalf("delivered %d events, want %d", len(got), n+1)
	}
	if last := got[len(got)-1]; last.Kind != engine.EventPipelineCompleted {
		t.Errorf("terminal event dropped; last delivered = %q", last.Kind)
	}
}

// TestSubscribeSinceFiltersHistory checks the SSE resume cursor: a finished
// run replays only events with seq > since, still including the terminal
// event, while since=0 replays the full history (T3).
func TestSubscribeSinceFiltersHistory(t *testing.T) {
	r := &Run{
		status:      RunCompleted,
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
	}
	const n = 200
	for i := 0; i < n; i++ {
		r.history = append(r.history, engine.Event{Kind: engine.EventStageProgress, Seq: int64(i + 1)})
	}
	r.history = append(r.history, engine.Event{Kind: engine.EventPipelineCompleted, Seq: n + 1})

	var since int64 = 150
	var got []engine.Event
	for ev := range r.Subscribe(since) {
		got = append(got, ev)
	}
	if len(got) != n+1-int(since) {
		t.Fatalf("delivered %d events, want %d", len(got), n+1-int(since))
	}
	for _, ev := range got {
		if ev.Seq <= since {
			t.Errorf("event with seq %d <= since %d leaked through", ev.Seq, since)
		}
	}
	if last := got[len(got)-1]; last.Kind != engine.EventPipelineCompleted {
		t.Errorf("terminal event dropped; last = %q", last.Kind)
	}

	all := 0
	for range r.Subscribe(0) {
		all++
	}
	if all != n+1 {
		t.Fatalf("since=0 delivered %d, want full %d", all, n+1)
	}
}

// TestSubscribeRaceOnCancelledRunningRun exercises concurrent Subscribe calls
// against a cancelled-but-running run whose fanOutEvents loop is still
// appending to history. Run under -race: Subscribe must not read r.history
// outside r.mu.
func TestSubscribeRaceOnCancelledRunningRun(t *testing.T) {
	r := &Run{
		status:      RunCancelled, // terminal status, but fanOutEvents still appends
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
	}

	src := make(chan engine.Event)
	done := make(chan struct{})
	go r.fanOutEvents(src, done)
	go func() {
		for i := 0; i < 1000; i++ {
			src <- engine.Event{Kind: engine.EventStageProgress}
		}
		close(src)
	}()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				ch := r.Subscribe(0)
				for range ch { // finished run: channel is closed after replay
				}
			}
		}()
	}
	wg.Wait()
	<-done
}
