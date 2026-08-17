package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/engine"
)

// writeRunDir materializes a run dir under root: run.json + events.jsonl.
// A nil events slice writes no terminal event (a still-running run).
func writeRunDir(t *testing.T, root, id, graph string, started time.Time, events []engine.Event) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := engine.Manifest{RunID: id, GraphName: graph, StartedAt: started}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "run.json"), data, 0o644); err != nil {
		t.Fatal(err)
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
	f.Close()
	return dir
}

func TestListRuns(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)

	writeRunDir(t, root, "run-ok", "alpha", base, []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1, RunID: "run-ok"},
		{Kind: engine.EventPipelineCompleted, Seq: 2, RunID: "run-ok", Status: "success"},
	})
	writeRunDir(t, root, "run-bad", "beta", base.Add(1*time.Hour), []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1, RunID: "run-bad"},
		{Kind: engine.EventPipelineFailed, Seq: 2, RunID: "run-bad", Message: "boom"},
	})
	writeRunDir(t, root, "run-live", "gamma", base.Add(2*time.Hour), []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1, RunID: "run-live"},
	})
	// Malformed: garbage run.json, still a directory under the root.
	mal := filepath.Join(root, "run-mal")
	if err := os.MkdirAll(mal, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mal, "run.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := listRuns(root)
	if len(got) != 4 {
		t.Fatalf("listRuns returned %d entries, want 4:\n%+v", len(got), got)
	}

	// Most-recent-first: run-live (base+2h) > run-bad (base+1h) > run-ok
	// (base). The malformed dir has no started-at → falls back to mtime
	// (created last in this test), so it sorts to the front.
	byID := map[string]runSummary{}
	var order []string
	for _, r := range got {
		byID[r.ID] = r
		order = append(order, r.ID)
	}

	if byID["run-ok"].Status != "success" || byID["run-ok"].Graph != "alpha" {
		t.Errorf("run-ok: got %+v, want status success graph alpha", byID["run-ok"])
	}
	if byID["run-bad"].Status != "failed" || byID["run-bad"].Graph != "beta" {
		t.Errorf("run-bad: got %+v, want status failed graph beta", byID["run-bad"])
	}
	if byID["run-live"].Status != "running" || byID["run-live"].Graph != "gamma" {
		t.Errorf("run-live: got %+v, want status running graph gamma", byID["run-live"])
	}
	if byID["run-mal"].Status != "unknown" || byID["run-mal"].ID != "run-mal" {
		t.Errorf("run-mal: got %+v, want status unknown id run-mal", byID["run-mal"])
	}

	// Ordering among the timestamped runs is most-recent-first.
	idx := map[string]int{}
	for i, id := range order {
		idx[id] = i
	}
	if !(idx["run-live"] < idx["run-bad"] && idx["run-bad"] < idx["run-ok"]) {
		t.Errorf("order not most-recent-first: %v", order)
	}
}

func TestListRunsMissingRoot(t *testing.T) {
	got := listRuns(filepath.Join(t.TempDir(), "does-not-exist"))
	if len(got) != 0 {
		t.Fatalf("missing root: got %d entries, want 0", len(got))
	}
}
