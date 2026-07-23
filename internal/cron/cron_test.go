package cron

import (
	"testing"
	"time"
)

// TestStepStarFieldIsUnrestricted checks Vixie semantics: a field beginning
// with `*` is unrestricted regardless of a step, so `*/n` does not set the
// restricted flag.
func TestStepStarFieldIsUnrestricted(t *testing.T) {
	_, restricted, err := parseField("*/2", 1, 31)
	if err != nil {
		t.Fatalf("parseField: %v", err)
	}
	if restricted {
		t.Errorf("`*/2` marked restricted; Vixie treats any `*`-field as unrestricted")
	}
}

// TestStepStarDomNoUnionWidening is the worked example from the finding:
// `0 0 */2 * 0` — dom `*/2` is unrestricted, so the dom/dow union rule must
// NOT widen matching. Every firing must be BOTH an odd day-of-month AND a
// Sunday (logical AND), not either-or.
func TestStepStarDomNoUnionWidening(t *testing.T) {
	s, err := Parse("0 0 */2 * 0")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	matched := 0
	for d := start; d.Before(end); d = d.Add(time.Hour) {
		if !s.matches(d) {
			continue
		}
		matched++
		if d.Weekday() != time.Sunday || d.Day()%2 == 0 {
			t.Errorf("matched %v (weekday=%v day=%d); expected odd-day Sundays only",
				d.Format(time.RFC3339), d.Weekday(), d.Day())
		}
	}
	if matched == 0 {
		t.Fatal("no matches in window; test would not detect the union-widening bug")
	}
}
