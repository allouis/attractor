package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestReload_SkipsIdlessManifests verifies the registry ignores run dirs
// whose manifest has no id — standalone `attractor run` dirs sharing the
// logs root. Without the skip they all collapse into a single broken
// r.runs[""] entry that surfaces as an empty, unopenable row in the fleet
// view.
func TestReload_SkipsIdlessManifests(t *testing.T) {
	base := t.TempDir()
	writeManifestDir(t, base, "daemon-run", Manifest{ID: "run123", Status: RunCompleted, GraphName: "demo"})
	writeManifestDir(t, base, "standalone-a", Manifest{ID: "", GraphName: "task"})
	writeManifestDir(t, base, "standalone-b", Manifest{ID: "", GraphName: "build"})

	r := newRunRegistry(base)
	runs := r.List()
	if len(runs) != 1 {
		var ids []string
		for _, run := range runs {
			ids = append(ids, run.ID)
		}
		t.Fatalf("want exactly the id-bearing run, got %d: %v", len(runs), ids)
	}
	if runs[0].ID != "run123" {
		t.Fatalf("loaded the wrong run: %q", runs[0].ID)
	}
}

// TestReload_ManifestOwnershipLayouts pins reload behavior across the three
// run-dir layouts P5a's manifest ownership split produces (ui-run-view-v3 P5a):
//
//   - old dual-writer dir: a daemon manifest.json carrying an id (a completed
//     direct run whose daemon restored the id at terminal) → loads.
//   - new split dir: a daemon manifest.json with id PLUS the engine's run.json
//     identity record → loads from manifest.json; run.json is ignored by reload.
//   - CLI-only dir: a standalone `attractor run --logs` dir that has only the
//     engine's id-less run.json (new) or an id-less engine-schema manifest.json
//     (old) → skipped, exactly as today (no fleet regression).
func TestReload_ManifestOwnershipLayouts(t *testing.T) {
	base := t.TempDir()
	writeManifestDir(t, base, "old-dual", Manifest{ID: "old1", Status: RunCompleted})
	writeSplitDir(t, base, "new-split", Manifest{ID: "new1", Status: RunCompleted})
	writeRunJSONDir(t, base, "cli-new") // only run.json, no manifest.json
	// CLI-only (old): an id-less engine-schema manifest.json.
	writeManifestDir(t, base, "cli-old", Manifest{ID: "", GraphName: "task"})

	r := newRunRegistry(base)
	got := map[string]bool{}
	for _, run := range r.List() {
		got[run.ID] = true
	}
	if len(got) != 2 || !got["old1"] || !got["new1"] {
		t.Fatalf("reload loaded %v, want exactly {old1, new1}", got)
	}
}

// writeSplitDir writes a new-layout run dir: the daemon's manifest.json plus the
// engine's run.json identity record alongside it.
func writeSplitDir(t *testing.T, base, name string, m Manifest) {
	t.Helper()
	writeManifestDir(t, base, name, m)
	writeRunJSON(t, filepath.Join(base, name))
}

// writeRunJSONDir writes a CLI-only run dir: the engine's run.json and no
// daemon manifest.json.
func writeRunJSONDir(t *testing.T, base, name string) {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunJSON(t, dir)
}

// writeRunJSON writes an engine-schema run.json (no `id` field) into dir.
func writeRunJSON(t *testing.T, dir string) {
	t.Helper()
	data := []byte(`{"run_id":"child","graph_name":"demo","goal":"g"}`)
	if err := os.WriteFile(filepath.Join(dir, "run.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeManifestDir(t *testing.T, base, name string, m Manifest) {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}
