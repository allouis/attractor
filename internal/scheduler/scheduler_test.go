package scheduler

import (
	"bytes"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/automation"
)

// TestTickReportsFireError verifies that when an automation's submit fails,
// the scheduler surfaces the error instead of silently dropping it.
func TestTickReportsFireError(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	a := automation.Automation{Name: "boom", Cron: "* * * * *"}
	fire := func(automation.Automation) (string, error) {
		return "", errors.New("submit failed")
	}
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	s := New([]automation.Automation{a}, fire, func() time.Time { return base })

	if n := s.Tick(base.Add(time.Minute)); n != 1 {
		t.Fatalf("due tick fired %d, want 1", n)
	}
	out := buf.String()
	if !strings.Contains(out, "submit failed") || !strings.Contains(out, "boom") {
		t.Errorf("fire error not surfaced; log output = %q", out)
	}
}
