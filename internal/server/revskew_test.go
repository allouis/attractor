package server

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/version"
)

func TestWarnRevSkew(t *testing.T) {
	daemon := version.Get() // whatever this build reports

	capture := func() *bytes.Buffer {
		var buf bytes.Buffer
		log.SetOutput(&buf)
		return &buf
	}
	t.Cleanup(func() { log.SetOutput(log.Writer()) })

	run := &Run{ID: "r1"}

	// Same rev → no warning.
	buf := capture()
	run.warnRevSkew(daemon)
	if buf.Len() != 0 {
		t.Fatalf("warned on matching rev: %q", buf.String())
	}

	// Unknown child → no warning (nothing to compare).
	buf = capture()
	run.warnRevSkew("")
	run.warnRevSkew("unknown")
	if buf.Len() != 0 {
		t.Fatalf("warned on unknown rev: %q", buf.String())
	}

	// Different rev → warns, but only once.
	if daemon == "unknown" {
		t.Skip("daemon build has no revision to compare against")
	}
	buf = capture()
	run.warnRevSkew("deadbeef0000")
	run.warnRevSkew("deadbeef0000")
	if got := strings.Count(buf.String(), "different attractor"); got != 1 {
		t.Fatalf("skew warnings = %d, want exactly 1; log=%q", got, buf.String())
	}
}
