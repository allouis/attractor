package server

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// TestLocalLauncherResumesFromCheckpoint is the strongest resume assertion
// available without KVM: a real local child (its own `attractor run` process,
// phoning home) fails at a node, then a restart resumes it from the host-side
// checkpoint — the SAME engine path a VM child takes. It proves the whole
// launcher-backed revive chain end to end: resumable() lets the failed local
// run restart; the local launcher seeds the child's fresh logs dir with the
// daemon's checkpoint.json; and the resumed engine skips the completed node
// (its side effect does NOT repeat) and re-runs the failed one to completion.
func TestLocalLauncherResumesFromCheckpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the attractor binary")
	}
	bin := buildAttractor(t)
	logs := t.TempDir()
	local := localLauncher{bin: bin}
	srv := New(Config{
		Addr: "127.0.0.1:0", LogsRoot: logs,
		Launcher:  local,
		Launchers: map[string]Launcher{"local": local},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Close()

	scratch := t.TempDir()
	counter := filepath.Join(scratch, "counter")
	sentinel := filepath.Join(scratch, "sentinel")
	// Node a appends to counter (its side effect must run exactly once across the
	// two attempts); node b fails on its first visit and succeeds on the second.
	pipeline := `digraph d {
		start [shape=Mdiamond]
		a [type="tool", tool_command="echo x >> ` + counter + `"]
		b [type="tool", tool_command="[ -f ` + sentinel + ` ] || { touch ` + sentinel + `; exit 1; }"]
		done [shape=Msquare]
		start -> a -> b -> done
	}`

	work := t.TempDir()
	id, err := srv.submit(pipeline, nil, work, "", "", "", "", "local", "")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	run := waitTerminal(t, srv, id, 30*time.Second)
	if run.Status() != RunFailed {
		t.Fatalf("first run status = %v, want failed (failure=%q)", run.Status(), run.failure)
	}
	if n := countLines(t, counter); n != 1 {
		t.Fatalf("counter = %d after first run, want 1", n)
	}

	// The failed local run must be revivable (host-side checkpoint + fields).
	if !run.Summary()["resumable"].(bool) {
		t.Fatal("failed local run not marked resumable — the launcher revive gate is off")
	}
	resp := postRestart(t, srv.URL(), id)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("restart status = %d, want 202", resp.StatusCode)
	}
	pollRunUntil(t, srv.URL(), id, RunCompleted)
	if n := countLines(t, counter); n != 1 {
		t.Errorf("counter = %d after resume, want 1 (node a must not re-run — checkpoint skip)", n)
	}
}
