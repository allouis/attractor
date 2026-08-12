package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/config"
	"github.com/allouis/attractor/internal/engine"
)

// readCheckpointContext returns the `context` map stored in a run's on-disk
// checkpoint.json.
func readCheckpointContext(t *testing.T, root string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Context map[string]string `json:"context"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v.Context
}

// overlayCheckContext must reach the on-disk checkpoint the resume reads (the
// engine restores a resumed run's context solely from checkpoint.ContextValues,
// not initialContext), overlay initialContext too, and preserve every other
// checkpoint field.
func TestOverlayCheckContext_ReachesCheckpointAndPreservesFields(t *testing.T) {
	root := t.TempDir()
	orig := `{
	  "current_node": "b",
	  "completed_nodes": ["start", "a"],
	  "context": {"check.test": "echo OLD", "repo": "a/b", "goal": "x"}
	}`
	if err := os.WriteFile(filepath.Join(root, "checkpoint.json"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	run := &Run{logsRoot: root, initialContext: map[string]string{"check.test": "echo OLD"}}

	run.overlayCheckContext(map[string]string{"check.test": "echo NEW", "check.lint": "echo LINT"})

	ctx := readCheckpointContext(t, root)
	if ctx["check.test"] != "echo NEW" {
		t.Errorf("checkpoint check.test = %q, want echo NEW", ctx["check.test"])
	}
	if ctx["check.lint"] != "echo LINT" {
		t.Errorf("checkpoint check.lint = %q, want echo LINT", ctx["check.lint"])
	}
	// Non-check context preserved.
	if ctx["repo"] != "a/b" || ctx["goal"] != "x" {
		t.Errorf("checkpoint context lost non-check keys: %v", ctx)
	}
	// Other checkpoint fields preserved.
	var raw map[string]json.RawMessage
	data, _ := os.ReadFile(filepath.Join(root, "checkpoint.json"))
	_ = json.Unmarshal(data, &raw)
	if _, ok := raw["completed_nodes"]; !ok {
		t.Error("completed_nodes dropped from checkpoint")
	}
	if run.initialContext["check.test"] != "echo NEW" {
		t.Errorf("initialContext check.test = %q, want echo NEW", run.initialContext["check.test"])
	}
}

// Restart re-derives check.* from LIVE config: an operator who changed a repo's
// test command in config between the failed run and the restart gets the NEW
// command in the resumed run's context (checks are policy, not run identity).
func TestReseedChecks_UsesLiveConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoDir := t.TempDir()
	doc := config.Document{Repos: map[string]config.RepoConfig{
		"a/b": {Path: repoDir, Checks: map[string]string{"test": "echo OLD"}},
	}}
	mustNil(t, doc.Save(home))

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "checkpoint.json"),
		[]byte(`{"context":{"check.test":"echo OLD"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir()})
	run := &Run{ID: "r", repo: "a/b", cwd: repoDir, logsRoot: root, initialContext: map[string]string{"check.test": "echo OLD"}}

	// Operator edits the check command in live config after the run failed.
	doc.Repos["a/b"] = config.RepoConfig{Path: repoDir, Checks: map[string]string{"test": "echo NEW"}}
	mustNil(t, doc.Save(home))

	srv.reseedChecks(run)

	if got := readCheckpointContext(t, root)["check.test"]; got != "echo NEW" {
		t.Errorf("checkpoint check.test = %q, want echo NEW (live config)", got)
	}
}

// The restart handler wires reseed in: a resumable launcher-backed run picks up
// the operator's new check command on restart.
func TestRestartHandler_ReseedsChecks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoDir := t.TempDir()
	doc := config.Document{Repos: map[string]config.RepoConfig{
		"a/b": {Path: repoDir, Checks: map[string]string{"test": "echo OLD"}},
	}}
	mustNil(t, doc.Save(home))

	var seen []string
	local := recordingLauncher{name: "local", seen: &seen}
	srv := New(Config{
		Addr: "127.0.0.1:0", LogsRoot: t.TempDir(),
		Launcher:  local,
		Launchers: map[string]Launcher{"local": local},
	})
	mustNil(t, srv.Start())
	t.Cleanup(func() { srv.Close() })

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "checkpoint.json"),
		[]byte(`{"context":{"check.test":"echo OLD"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run := &Run{
		ID: "rr", placement: "local", repo: "a/b", cwd: repoDir, token: "t",
		logsRoot:       root,
		status:         RunFailed,
		persisted:      true,
		initialContext: map[string]string{"check.test": "echo OLD"},
		subscribers:    map[chan engine.Event]struct{}{},
		questions:      map[string]*pendingQuestion{},
	}
	srv.registry.mu.Lock()
	srv.registry.runs[run.ID] = run
	srv.registry.mu.Unlock()

	doc.Repos["a/b"] = config.RepoConfig{Path: repoDir, Checks: map[string]string{"test": "echo NEW"}}
	mustNil(t, doc.Save(home))

	resp := postRestart(t, srv.URL(), run.ID)
	resp.Body.Close()

	if got := readCheckpointContext(t, root)["check.test"]; got != "echo NEW" {
		t.Errorf("after restart, checkpoint check.test = %q, want echo NEW", got)
	}
}
