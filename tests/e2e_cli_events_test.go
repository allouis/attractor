package attractor_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/cli"
	"github.com/allouis/attractor/internal/engine"
)

// TestCLI_RunPersistsEvents confirms a standalone `run` writes
// events.jsonl to its logs root, just like the server does. The file
// is written by the engine so both entry points persist events and any
// run is replayable in the UI (service-spec §3).
func TestCLI_RunPersistsEvents(t *testing.T) {
	logsRoot := t.TempDir()
	args := []string{"--logs", logsRoot, "../testdata/pipelines/smoke.dot"}
	if err := cli.Run(args); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	f, err := os.Open(filepath.Join(logsRoot, "events.jsonl"))
	if err != nil {
		t.Fatalf("events.jsonl missing: %v", err)
	}
	defer f.Close()

	var kinds []engine.EventKind
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev engine.Event
		must(t, json.Unmarshal(scanner.Bytes(), &ev))
		kinds = append(kinds, ev.Kind)
	}
	if len(kinds) < 3 {
		t.Fatalf("expected several event lines, got %d", len(kinds))
	}
	if kinds[0] != engine.EventPipelineStarted {
		t.Fatalf("first event = %q, want pipeline_started", kinds[0])
	}
	if last := kinds[len(kinds)-1]; last != engine.EventPipelineCompleted {
		t.Fatalf("last event = %q, want pipeline_completed", last)
	}
}
