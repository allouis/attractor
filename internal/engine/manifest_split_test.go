package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const splitGraph = `digraph d {
	start [shape=Mdiamond]
	work [type="probe"]
	done [shape=Msquare]
	start -> work
	work -> done
}`

// TestEngineWritesRunJSONNotManifest proves the engine's identity record is
// run.json (ui-run-view-v3 P5a): the engine writes run.json and never touches
// a daemon-written manifest.json sharing the same logs root, so the direct
// runner's clobber window — both writing manifest.json — is closed.
func TestEngineWritesRunJSONNotManifest(t *testing.T) {
	logs := t.TempDir()
	// A daemon-style manifest.json with an id, as the direct launcher stamps at
	// run creation before handing the same logs root to the engine.
	daemonManifest := []byte(`{"id":"daemon123","status":"running"}`)
	if err := os.WriteFile(filepath.Join(logs, "manifest.json"), daemonManifest, 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	reg.Register("start", okHandler{})
	reg.Register("probe", okHandler{})
	eng := New(Config{Registry: reg, LogsRoot: logs, RunID: "eng123"})
	if _, err := eng.Run(&PreparedGraph{Graph: buildGraph(t, splitGraph)}); err != nil {
		t.Fatalf("run: %v", err)
	}

	// The engine wrote run.json (its identity record).
	if _, err := os.Stat(filepath.Join(logs, "run.json")); err != nil {
		t.Fatalf("engine did not write run.json: %v", err)
	}

	// The daemon's manifest.json is byte-for-byte untouched — no clobber.
	got, err := os.ReadFile(filepath.Join(logs, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	if string(got) != string(daemonManifest) {
		t.Fatalf("engine clobbered daemon manifest.json: got %q, want %q", got, daemonManifest)
	}
}

// The reserved run.name context key (the CLI's --name flag) is stamped
// into run.json as a human label so the hub can show it instead of an
// opaque run id.
func TestEngineStampsRunName(t *testing.T) {
	logs := t.TempDir()
	reg := NewRegistry()
	reg.Register("start", okHandler{})
	reg.Register("probe", okHandler{})
	eng := New(Config{
		Registry:       reg,
		LogsRoot:       logs,
		RunID:          "eng123",
		InitialContext: map[string]string{"run.name": "envconfig bug"},
	})
	if _, err := eng.Run(&PreparedGraph{Graph: buildGraph(t, splitGraph)}); err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(logs, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "envconfig bug" {
		t.Fatalf("run.json Name = %q, want %q", m.Name, "envconfig bug")
	}
}
