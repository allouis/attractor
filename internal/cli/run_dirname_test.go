package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/engine"
)

// A default run names its directory after the same id it stamps into
// run.json, so `attractor runs` (which keys off the dir name) prints an
// id that `attractor view <dir>` can consume.
func TestRunDefaultDirNameMatchesRunID(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)

	dot := filepath.Join(t.TempDir(), "p.dot")
	if err := os.WriteFile(dot, []byte("digraph d {\n  start [shape=Mdiamond]\n  done [shape=Msquare]\n  start -> done\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"--backend", "simulation", dot}); err != nil {
		t.Fatalf("run: %v", err)
	}

	runsRoot := filepath.Join(data, "attractor", "runs")
	entries, err := os.ReadDir(runsRoot)
	if err != nil {
		t.Fatalf("read runs root: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d run dirs, want 1", len(entries))
	}
	dirName := entries[0].Name()

	raw, err := os.ReadFile(filepath.Join(runsRoot, dirName, "run.json"))
	if err != nil {
		t.Fatalf("read run.json: %v", err)
	}
	var m engine.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode run.json: %v", err)
	}
	if m.RunID != dirName {
		t.Fatalf("run dir %q != run.json RunID %q", dirName, m.RunID)
	}
}
