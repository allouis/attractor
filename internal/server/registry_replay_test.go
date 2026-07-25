package server

import (
	"sync"
	"testing"

	"github.com/fabro/attractor/internal/engine"
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

	ch := r.Subscribe()
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
				ch := r.Subscribe()
				for range ch { // finished run: channel is closed after replay
				}
			}
		}()
	}
	wg.Wait()
	<-done
}
