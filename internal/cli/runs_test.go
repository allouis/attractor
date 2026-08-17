package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/engine"
)

// writeRunDir materializes a run dir named dirName holding a run.json
// (with the given manifest RunID) and events.jsonl. A nil events slice
// writes no terminal event (a still-running run). dirName and manifestID
// are separate so tests can pin that the listing keys off the directory,
// not the manifest.
func writeRunDir(t *testing.T, root, dirName, manifestID, graph string, started time.Time, events []engine.Event) string {
	t.Helper()
	dir := filepath.Join(root, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := engine.Manifest{RunID: manifestID, GraphName: graph, StartedAt: started}
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

	writeRunDir(t, root, "run-ok", "run-ok", "alpha", base, []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1, RunID: "run-ok"},
		{Kind: engine.EventPipelineCompleted, Seq: 2, RunID: "run-ok", Status: "success"},
	})
	writeRunDir(t, root, "run-bad", "run-bad", "beta", base.Add(1*time.Hour), []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1, RunID: "run-bad"},
		{Kind: engine.EventPipelineFailed, Seq: 2, RunID: "run-bad", Message: "boom"},
	})
	writeRunDir(t, root, "run-live", "run-live", "gamma", base.Add(2*time.Hour), []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1, RunID: "run-live"},
	})
	// The directory name differs from the manifest RunID (runs are minted
	// with independent ids); the listing must key off the directory so the
	// id it prints is the one `view <dir>` consumes.
	writeRunDir(t, root, "dir-name", "manifest-id", "delta", base.Add(30*time.Minute), []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1},
		{Kind: engine.EventPipelineCompleted, Seq: 2, Status: "success"},
	})
	// Malformed: truncated run.json that leaves a partial RunID; must still
	// be reported unknown, not "running".
	partial := filepath.Join(root, "run-mal")
	if err := os.MkdirAll(partial, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "run.json"), []byte(`{"run_id":"r1","graph_name":`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Corrupt events: valid run.json, garbage events.jsonl → unknown.
	corrupt := writeRunDir(t, root, "run-corrupt", "run-corrupt", "eps", base.Add(3*time.Hour), nil)
	if err := os.WriteFile(filepath.Join(corrupt, "events.jsonl"), []byte("garbage\nmore\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := listRuns(root)
	if len(got) != 6 {
		t.Fatalf("listRuns returned %d entries, want 6:\n%+v", len(got), got)
	}

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
	// Keyed by dir name, not manifest id.
	if _, ok := byID["dir-name"]; !ok {
		t.Errorf("run listed under manifest id, not dir name: %v", order)
	}
	if _, ok := byID["manifest-id"]; ok {
		t.Errorf("run listed under manifest id %q; must key off dir name", "manifest-id")
	}
	if byID["run-mal"].Status != "unknown" {
		t.Errorf("partial run.json: got status %q, want unknown", byID["run-mal"].Status)
	}
	if byID["run-corrupt"].Status != "unknown" {
		t.Errorf("corrupt events: got status %q, want unknown", byID["run-corrupt"].Status)
	}

	// Ordering among the timestamped runs is most-recent-first.
	idx := map[string]int{}
	for i, id := range order {
		idx[id] = i
	}
	if !(idx["run-corrupt"] < idx["run-live"] && idx["run-live"] < idx["run-bad"] && idx["run-bad"] < idx["run-ok"]) {
		t.Errorf("order not most-recent-first: %v", order)
	}
}

func TestListRunsMissingRoot(t *testing.T) {
	got := listRuns(filepath.Join(t.TempDir(), "does-not-exist"))
	if len(got) != 0 {
		t.Fatalf("missing root: got %d entries, want 0", len(got))
	}
}

// printRuns must not let a crafted graph_name inject control characters or
// break the one-line-per-run layout.
func TestPrintRunsSanitizesFields(t *testing.T) {
	var b strings.Builder
	printRuns(&b, []runSummary{
		{ID: "r1", Graph: "evil\n\x1b[31mINJECT", Status: "success", StartedAt: time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)},
	})
	out := b.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("output not one line per run:\n%q", out)
	}
	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("terminal escape not stripped:\n%q", out)
	}
}
