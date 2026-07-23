package attractor_test

import (
	"testing"
	"time"

	"github.com/fabro/attractor/internal/cron"
)

func TestCron_ParseErrors(t *testing.T) {
	bad := []string{
		"",
		"* * * *",     // 4 fields
		"* * * * * *", // 6 fields
		"60 * * * *",  // minute out of range
		"* 24 * * *",  // hour out of range
		"* * 0 * *",   // dom out of range (1-31)
		"* * * 13 *",  // month out of range
		"* * * * 7",   // dow out of range (0-6)
		"*/0 * * * *", // zero step
		"5-1 * * * *", // inverted range
		"a * * * *",   // non-numeric
	}
	for _, spec := range bad {
		if _, err := cron.Parse(spec); err == nil {
			t.Errorf("Parse(%q) expected error, got nil", spec)
		}
	}
}

func TestCron_NextDaily(t *testing.T) {
	sched, err := cron.Parse("0 3 * * *")
	must(t, err)
	// After 01:00 the next fire is 03:00 same day.
	after := time.Date(2026, 7, 23, 1, 0, 0, 0, time.Local)
	got := sched.Next(after)
	want := time.Date(2026, 7, 23, 3, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
	// After 03:00 (inclusive of the matching minute) the next fire is the
	// following day, since Next is strictly after the argument.
	got = sched.Next(want)
	want2 := time.Date(2026, 7, 24, 3, 0, 0, 0, time.Local)
	if !got.Equal(want2) {
		t.Fatalf("Next after match = %v, want %v", got, want2)
	}
}

func TestCron_NextStep(t *testing.T) {
	sched, err := cron.Parse("*/15 * * * *")
	must(t, err)
	after := time.Date(2026, 7, 23, 10, 7, 30, 0, time.Local)
	got := sched.Next(after)
	want := time.Date(2026, 7, 23, 10, 15, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestCron_NextDayOfWeek(t *testing.T) {
	// Mondays at 09:00. 2026-07-23 is a Thursday; next Monday is 07-27.
	sched, err := cron.Parse("0 9 * * 1")
	must(t, err)
	after := time.Date(2026, 7, 23, 12, 0, 0, 0, time.Local)
	got := sched.Next(after)
	want := time.Date(2026, 7, 27, 9, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Fatalf("Next = %v (weekday %v), want %v", got, got.Weekday(), want)
	}
}

func TestCron_DomOrDowUnion(t *testing.T) {
	// Both dom and dow restricted -> Vixie OR semantics: fire on the 1st
	// OR on any Monday. 2026-07-23 Thu -> next match is Mon 07-27, but the
	// 1st of next month (Sat 08-01) is not the nearest; a mid-run Monday
	// wins.
	sched, err := cron.Parse("0 0 1 * 1")
	must(t, err)
	after := time.Date(2026, 7, 23, 0, 0, 0, 0, time.Local)
	got := sched.Next(after)
	want := time.Date(2026, 7, 27, 0, 0, 0, 0, time.Local) // Monday
	if !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}
