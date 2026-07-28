package attractor_test

import (
	"testing"
	"time"

	"github.com/allouis/attractor/internal/automation"
	"github.com/allouis/attractor/internal/scheduler"
)

func mustAuto(t *testing.T, name, src string) automation.Automation {
	t.Helper()
	a, err := automation.Parse(name, []byte(src))
	must(t, err)
	return a
}

func TestScheduler_TickFiresWhenDue(t *testing.T) {
	a := mustAuto(t, "m", "pipeline = \"p.dot\"\n[trigger]\ncron = \"* * * * *\"\n")
	base := time.Date(2026, 7, 23, 10, 0, 30, 0, time.Local)
	var fired []string
	s := scheduler.New([]automation.Automation{a},
		func(x automation.Automation) (string, error) {
			fired = append(fired, x.Name)
			return "id", nil
		},
		func() time.Time { return base },
	)
	// First occurrence after 10:00:30 is 10:01. A tick still at 10:00:30
	// fires nothing.
	if n := s.Tick(base); n != 0 {
		t.Fatalf("early tick fired %d, want 0", n)
	}
	// A tick at 10:01 fires the automation once.
	if n := s.Tick(base.Add(30 * time.Second).Add(time.Minute)); n != 1 {
		t.Fatalf("due tick fired %d, want 1", n)
	}
	if len(fired) != 1 || fired[0] != "m" {
		t.Fatalf("fired = %v", fired)
	}
	// A later tick within the same minute does not refire.
	if n := s.Tick(base.Add(90 * time.Second)); n != 0 {
		t.Fatalf("refired within same minute: %d", n)
	}
}

func TestScheduler_SkipsManualOnly(t *testing.T) {
	// An automation with no cron trigger is manual-only: never scheduled.
	a := mustAuto(t, "x", "pipeline = \"p.dot\"\n")
	s := scheduler.New([]automation.Automation{a},
		func(automation.Automation) (string, error) { return "", nil },
		func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local) },
	)
	future := time.Date(2027, 1, 1, 0, 0, 0, 0, time.Local)
	if n := s.Tick(future); n != 0 {
		t.Fatalf("manual-only automation fired %d, want 0", n)
	}
}
