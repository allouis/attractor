package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
)

// TestChildWritesNoDaemonOwnedArtifacts pins the single-writer invariant P5c
// relies on (ui-run-view-v3 P5c): when the child's logs root is the daemon's
// run dir shared over rw 9p, the child must never write a file the daemon owns
// (manifest.json / source.dot / events.jsonl), or the two writers would clobber.
// Modelled by running the engine exactly as the vm child does — reporting mode
// with the event log suppressed (--no-event-log) — and asserting nothing it
// produces is a daemonOwnedArtifact, while its own identity record (run.json) is
// present.
func TestChildWritesNoDaemonOwnedArtifacts(t *testing.T) {
	logs := t.TempDir()
	err := runEngineReporting(prepareToolGraph(t), fake.New(), nil, logs, false,
		map[string]string{"k": "v"}, nil, true /* skipEventLog, as the vm child runs */)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	sawRunJSON := false
	err = filepath.WalkDir(logs, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(logs, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if daemonOwnedArtifact(rel) {
			t.Errorf("child wrote daemon-owned artifact %q into the shared logs root", rel)
		}
		if rel == "run.json" {
			sawRunJSON = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawRunJSON {
		t.Fatal("child did not write its run.json identity record")
	}
	// Belt-and-braces: the suppressed event log really is absent.
	if _, err := os.Stat(filepath.Join(logs, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("events.jsonl present despite --no-event-log (err=%v)", err)
	}
}
