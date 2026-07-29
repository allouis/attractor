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
