package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/engine"
)

// writeArchivedRun materializes a completed run dir under
// <hubDir>/runs/<runID> with a chosen StartedAt, as archivedDoc would
// unpack it. Used to pin the /runs ordering.
func writeArchivedRun(t *testing.T, hubDir, runID string, started time.Time) {
	t.Helper()
	dir := filepath.Join(hubDir, "runs", runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunDirAt(t, dir, runID, "completed", started)
}

// writeRunDir materializes a minimal run dir: run.json + events.jsonl.
// status "completed" appends the terminal event.
func writeRunDir(t *testing.T, dir, runID, status string) {
	writeRunDirAt(t, dir, runID, status, time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC))
}

func writeRunDirAt(t *testing.T, dir, runID, status string, started time.Time) {
	t.Helper()
	m := engine.Manifest{RunID: runID, GraphName: "g", Goal: "fix", StartedAt: started}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "run.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	events := []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1, RunID: runID},
		{Kind: engine.EventStageStarted, Seq: 2, RunID: runID, NodeID: "work", Visit: 1, Attempt: 1},
		{Kind: engine.EventStageCompleted, Seq: 3, RunID: runID, NodeID: "work", Visit: 1, Attempt: 1, Status: "success"},
	}
	if status == "completed" {
		events = append(events, engine.Event{Kind: engine.EventPipelineCompleted, Seq: 4, RunID: runID, Status: "success"})
	}
	f, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
