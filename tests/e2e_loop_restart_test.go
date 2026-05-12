package attractor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fabro/attractor/internal/backend/fake"
	"github.com/fabro/attractor/internal/engine"
)

func TestLoopRestart_ArchivesAndResetsState(t *testing.T) {
	src := `digraph r {
		start [shape=Mdiamond]
		seed [prompt="seed"]
		check [shape=diamond]
		body [prompt="body"]
		done [shape=Msquare]
		start -> seed -> check
		check -> body [condition="context.go=on"]
		check -> done [condition="context.go!=on"]
		body -> seed [loop_restart=true]
	}`

	be := fake.New()
	// First seed: enable looping. After restart: disable it so the
	// pipeline exits via the check -> done branch.
	be.SetSequence("seed",
		fake.Step{Outcome: &engine.Outcome{Status: engine.StatusSuccess, ContextUpdates: map[string]string{"go": "on"}}},
		fake.Step{Outcome: &engine.Outcome{Status: engine.StatusSuccess, ContextUpdates: map[string]string{"go": "off"}}},
	)
	be.SetText("body", "body work")

	logsRoot := t.TempDir()
	out, events := runFixtureIn(t, src, be, nil, logsRoot)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q", out.Status, out.FailureReason)
	}
	// The first run's logs should have been archived to _restart_1/.
	if _, err := os.Stat(filepath.Join(logsRoot, "_restart_1")); err != nil {
		t.Fatalf("_restart_1 archive missing: %v", err)
	}
	if be.CallCount("seed") != 2 {
		t.Fatalf("seed should run twice (initial + post-restart), got %d", be.CallCount("seed"))
	}
	// Engine should have emitted the loop_restart progress event.
	sawRestartEvent := false
	for _, ev := range events {
		if ev.Kind == engine.EventStageProgress && contains(ev.Message, "loop_restart") {
			sawRestartEvent = true
		}
	}
	if !sawRestartEvent {
		t.Fatal("expected a loop_restart progress event")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
