package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestLocalRunArgs(t *testing.T) {
	tmp := t.TempDir()
	run := &Run{ID: "rid", token: "tok", cwd: "/work", initialContext: map[string]string{"b": "2", "a": "1"}}
	args := localRunArgs(run, "http://127.0.0.1:9/", filepath.Join(tmp, "source.dot"), tmp)

	want := []string{
		"run", "--report-to", "http://127.0.0.1:9/", "--run-id", "rid",
		"--report-token", "tok", "--logs", tmp, "--base-dir", "/work", "--cwd", "/work",
		"-var", "a=1", "-var", "b=2", filepath.Join(tmp, "source.dot"),
	}
	if !slices.Equal(args, want) {
		t.Fatalf("args =\n %v\nwant\n %v", args, want)
	}
}

// End-to-end local launcher: build the real binary, submit a tool-only
// pipeline through a daemon configured with localLauncher, and assert the
// run completes via phone-home — the child's events reach the daemon and
// its artifacts land under the daemon's logs root.
func TestLocalLauncherEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the attractor binary")
	}
	bin := buildAttractor(t)
	logs := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: logs, Launcher: localLauncher{bin: bin}})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Close()

	const pipeline = `digraph d {
		start [shape=Mdiamond]
		work [type="tool", tool_command="echo hi > marker.txt"]
		done [shape=Msquare]
		start -> work
		work -> done
	}`
	work := t.TempDir()
	id, err := srv.submit(pipeline, nil, work, "", "", "", "", "", "")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	run := waitTerminal(t, srv, id, 30*time.Second)
	if run.Status() != RunCompleted {
		t.Fatalf("status = %v, want completed (failure=%q)", run.Status(), run.failure)
	}
	// Tool ran in the working tree.
	if _, err := os.Stat(filepath.Join(work, "marker.txt")); err != nil {
		t.Fatalf("tool did not run in cwd: %v", err)
	}
	// Events reached the daemon's own events.jsonl.
	if _, err := os.Stat(filepath.Join(logs, id, "events.jsonl")); err != nil {
		t.Fatalf("daemon events.jsonl missing: %v", err)
	}
	// The tool stage's status artifact was uploaded to the daemon.
	if _, err := os.Stat(filepath.Join(logs, id, "work", "status.json")); err != nil {
		t.Fatalf("uploaded stage artifact missing: %v", err)
	}
}

func buildAttractor(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "attractor")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/allouis/attractor/cmd/attractor")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build attractor: %v", err)
	}
	return bin
}

func waitTerminal(t *testing.T, srv *Server, id string, d time.Duration) *Run {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		run, ok := srv.registry.Get(id)
		if ok && run.isTerminal() {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach terminal state within %s", id, d)
	return nil
}
