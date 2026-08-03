package server

import (
	"testing"
	"time"

	"github.com/allouis/attractor/internal/engine"
)

// TestCancelledQueuedRunClosesSubscribers verifies that cancelling a run
// that is still queued (never executed) closes its SSE subscriber
// channels, so streams end instead of blocking forever.
func TestCancelledQueuedRunClosesSubscribers(t *testing.T) {
	r := &Run{
		ID:          "queued-run",
		status:      RunQueued,
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
	}

	ch := r.Subscribe(0)
	r.Cancel()

	done := make(chan struct{})
	go func() {
		for range ch { // drains until the channel is closed
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber stream did not close after cancelling a queued run")
	}
}

// TestDispatchRecoversFromLaunchPanic proves a panic while launching one run
// does not kill the dispatch goroutine (and with it the whole daemon): a
// following run still launches. Guards the re-run-from-failure crash path
// where a non-relaunchable run reaches execute (web-ui-v2-spec U6).
func TestDispatchRecoversFromLaunchPanic(t *testing.T) {
	d := newDispatcher(1)
	ran := make(chan struct{})
	d.launch = func(r *Run) {
		if r.ID == "boom" {
			panic("launch blew up")
		}
		close(ran)
	}
	go d.run()
	defer d.close()

	d.enqueue(&Run{ID: "boom"})
	d.enqueue(&Run{ID: "ok"})

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch loop died after a launch panic; the next run never launched")
	}
}
