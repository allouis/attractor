package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/engine"
)

// writeRunDir materializes a minimal run dir: run.json + events.jsonl.
// status "completed" appends the terminal event.
func writeRunDir(t *testing.T, dir, runID, status string) {
	t.Helper()
	m := engine.Manifest{RunID: runID, GraphName: "g", Goal: "fix", StartedAt: time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)}
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
