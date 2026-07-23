package acp

import (
	"testing"
	"time"
)

// TestWatchdogDoesNotCountDuringSetup verifies the stall countdown only runs
// after start(): a slow session setup (initialize/session-new) that exceeds
// the stall timeout with no activity must not trip the watchdog, and once
// the turn is under way progress (pokes) keeps it alive.
func TestWatchdogDoesNotCountDuringSetup(t *testing.T) {
	fired := make(chan struct{}, 1)
	wd := newWatchdog(20*time.Millisecond, func() { fired <- struct{}{} })

	// Setup phase: longer than the stall timeout, no start() yet.
	time.Sleep(60 * time.Millisecond)
	select {
	case <-fired:
		t.Fatal("watchdog fired during setup, before start()")
	default:
	}

	// Turn begins; progress arrives faster than the timeout.
	wd.start()
	for i := 0; i < 6; i++ {
		time.Sleep(8 * time.Millisecond)
		wd.poke()
	}
	select {
	case <-fired:
		t.Fatal("watchdog fired while the turn was making progress")
	default:
	}
	wd.close()
}

// TestWatchdogFiresWhenIdleAfterStart confirms the watchdog still catches a
// genuine stall once the turn has started and goes quiet.
func TestWatchdogFiresWhenIdleAfterStart(t *testing.T) {
	fired := make(chan struct{}, 1)
	wd := newWatchdog(20*time.Millisecond, func() { fired <- struct{}{} })
	wd.start()
	select {
	case <-fired:
	case <-time.After(1 * time.Second):
		t.Fatal("watchdog did not fire on a genuine stall after start()")
	}
	wd.close()
}
