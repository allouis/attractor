package rundir

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/engine"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadManifest(t *testing.T) {
	dir := t.TempDir()

	// Missing file → error.
	if _, err := ReadManifest(dir); err == nil {
		t.Fatal("missing run.json: want error")
	}

	// Malformed (truncated) JSON → error, zero manifest (no partial fields
	// leaking through).
	writeFile(t, filepath.Join(dir, "run.json"), `{"run_id":"r1","graph_name":`)
	m, err := ReadManifest(dir)
	if err == nil {
		t.Fatal("truncated run.json: want error")
	}
	if m.RunID != "" {
		t.Fatalf("truncated run.json leaked RunID %q, want empty", m.RunID)
	}

	// Valid → parsed.
	data, _ := json.Marshal(engine.Manifest{RunID: "r1", GraphName: "g"})
	writeFile(t, filepath.Join(dir, "run.json"), string(data))
	m, err = ReadManifest(dir)
	if err != nil || m.RunID != "r1" || m.GraphName != "g" {
		t.Fatalf("valid run.json: got %+v err %v", m, err)
	}
}

func TestReadEvents(t *testing.T) {
	valid := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		f, _ := os.Create(filepath.Join(dir, "events.jsonl"))
		enc := json.NewEncoder(f)
		enc.Encode(engine.Event{Kind: engine.EventPipelineStarted, Seq: 1})
		enc.Encode(engine.Event{Kind: engine.EventPipelineCompleted, Seq: 2})
		f.Close()
		return dir
	}

	// Missing file → error.
	if _, err := ReadEvents(t.TempDir(), 0); err == nil {
		t.Fatal("missing events.jsonl: want error")
	}

	// Valid → events, no error.
	ev, err := ReadEvents(valid(t), 0)
	if err != nil || len(ev) != 2 {
		t.Fatalf("valid events: got %d err %v", len(ev), err)
	}

	// One malformed line among valid ones is skipped (self-heals), no error.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "events.jsonl"),
		`{"kind":"pipeline_started","seq":1}`+"\n"+`{not json`+"\n"+`{"kind":"pipeline_completed","seq":2}`+"\n")
	ev, err = ReadEvents(dir, 0)
	if err != nil {
		t.Fatalf("one torn line should not error: %v", err)
	}
	if len(ev) != 2 {
		t.Fatalf("skipped torn line: got %d events, want 2", len(ev))
	}

	// A non-empty log whose every line is garbage → error (corrupt, not a
	// fresh run).
	dir = t.TempDir()
	writeFile(t, filepath.Join(dir, "events.jsonl"), "garbage\nmore garbage\n")
	if _, err := ReadEvents(dir, 0); err == nil {
		t.Fatal("all-garbage events: want error")
	}
}
