// Package cron implements the five-field cron subset Attractor
// automations use for scheduled triggers (service-spec §5): five
// space-separated fields — minute hour day-of-month month day-of-week —
// each supporting `*`, lists (`a,b`), ranges (`a-b`), and steps (`*/n`,
// `a-b/n`). Times are interpreted in the local zone. It is dependency-
// free and deliberately small; anything richer (named months/days, `L`,
// `?`, `@hourly`) is unsupported.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed cron expression.
type Schedule struct {
	minute []bool // index 0-59
	hour   []bool // index 0-23
	dom    []bool // index 1-31 (index 0 unused)
	month  []bool // index 1-12 (index 0 unused)
	dow    []bool // index 0-6, 0=Sunday
	// domRestricted/dowRestricted record whether the field was narrower
	// than `*`. When both are restricted, a day matches if EITHER field
	// matches (Vixie cron union semantics).
	domRestricted bool
	dowRestricted bool
}

type fieldSpec struct {
	min, max int
}

var fields = []fieldSpec{
	{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // day of month
	{1, 12}, // month
	{0, 6},  // day of week
}

// Parse compiles a five-field cron spec. It errors on the wrong field
// count, out-of-range values, inverted ranges, or zero steps.
func Parse(spec string) (Schedule, error) {
	parts := strings.Fields(strings.TrimSpace(spec))
	if len(parts) != 5 {
		return Schedule{}, fmt.Errorf("cron: expected 5 fields, got %d in %q", len(parts), spec)
	}
	sets := make([][]bool, 5)
	restricted := make([]bool, 5)
	for i, part := range parts {
		set, isRestricted, err := parseField(part, fields[i].min, fields[i].max)
		if err != nil {
			return Schedule{}, fmt.Errorf("cron: field %d (%q): %w", i+1, part, err)
		}
		sets[i] = set
		restricted[i] = isRestricted
	}
	return Schedule{
		minute:        sets[0],
		hour:          sets[1],
		dom:           sets[2],
		month:         sets[3],
		dow:           sets[4],
		domRestricted: restricted[2],
		dowRestricted: restricted[4],
	}, nil
}

// parseField expands one field into a membership slice sized max+1. The
// returned bool reports whether the field was narrower than `*`.
func parseField(field string, min, max int) ([]bool, bool, error) {
	set := make([]bool, max+1)
	restricted := true
	for _, item := range strings.Split(field, ",") {
		rng := item
		step := 1
		if slash := strings.IndexByte(item, '/'); slash >= 0 {
			rng = item[:slash]
			s, err := strconv.Atoi(item[slash+1:])
			if err != nil || s <= 0 {
				return nil, false, fmt.Errorf("invalid step %q", item)
			}
			step = s
		}
		lo, hi := min, max
		if rng == "*" {
			// A field beginning with `*` is unrestricted regardless of any
			// step (Vixie/cronie semantics): `*/n` does not narrow the field
			// for the purpose of the dom/dow union rule.
			restricted = false
		} else if dash := strings.IndexByte(rng, '-'); dash >= 0 {
			a, err1 := strconv.Atoi(rng[:dash])
			b, err2 := strconv.Atoi(rng[dash+1:])
			if err1 != nil || err2 != nil {
				return nil, false, fmt.Errorf("invalid range %q", rng)
			}
			lo, hi = a, b
		} else {
			v, err := strconv.Atoi(rng)
			if err != nil {
				return nil, false, fmt.Errorf("invalid value %q", rng)
			}
			lo, hi = v, v
		}
		if lo < min || hi > max || lo > hi {
			return nil, false, fmt.Errorf("value out of range [%d,%d] in %q", min, max, item)
		}
		for v := lo; v <= hi; v += step {
			set[v] = true
		}
	}
	return set, restricted, nil
}

// Next returns the earliest time strictly after `after` that matches the
// schedule, in the same location as `after`. It scans minute by minute
// up to a one-year bound; an unreachable schedule returns the zero time.
func (s Schedule) Next(after time.Time) time.Time {
	// Start at the next whole minute after `after`.
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(1, 0, 0)
	for ; t.Before(limit); t = t.Add(time.Minute) {
		if s.matches(t) {
			return t
		}
	}
	return time.Time{}
}

// matches reports whether t satisfies every field. Day matching honours
// Vixie union semantics when both day fields are restricted.
func (s Schedule) matches(t time.Time) bool {
	if !s.minute[t.Minute()] || !s.hour[t.Hour()] || !s.month[int(t.Month())] {
		return false
	}
	domOK := s.dom[t.Day()]
	dowOK := s.dow[int(t.Weekday())]
	if s.domRestricted && s.dowRestricted {
		return domOK || dowOK
	}
	return domOK && dowOK
}
