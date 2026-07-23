package server

import (
	"testing"
	"time"

	"github.com/fabro/attractor/internal/engine"
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

	ch := r.Subscribe()
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
