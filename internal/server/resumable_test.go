package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/interviewer"
)

// writeCheckpoint drops a minimal checkpoint.json under a fresh logs root and
// returns the root, standing in for the host-side checkpoint a launcher-backed
// run leaves (child logs root = daemon run dir since P5c).
func writeCheckpoint(t *testing.T, ctx string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "checkpoint.json"), []byte(ctx), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// resumable gates the re-run-from-failure control. Direct runs need their
// engine deps in memory; launcher-backed (local/vm) runs instead need a
// host-side checkpoint plus the reloadable launch inputs AND the phone-home
// token (which is not persisted, so a disk-reloaded run cannot relaunch).
func TestResumable_GatingMatrix(t *testing.T) {
	stub := HandlerFactory(func(interviewer.Interviewer) *engine.Registry { return nil })
	ckpt := writeCheckpoint(t, `{"context":{}}`)

	cases := []struct {
		name string
		run  *Run
		want bool
	}{
		{"direct + deps in memory", &Run{placement: "direct", prepared: &engine.PreparedGraph{}, factory: stub}, true},
		{"default (empty) placement", &Run{placement: "", prepared: &engine.PreparedGraph{}, factory: stub}, true},
		{"direct reloaded shell (nil deps)", &Run{placement: "direct"}, false},
		{"vm no checkpoint/fields", &Run{placement: "vm", prepared: &engine.PreparedGraph{}, factory: stub}, false},
		{"vm checkpoint + cwd + token", &Run{placement: "vm", cwd: "/repo", token: "t", logsRoot: ckpt}, true},
		{"vm checkpoint + cwd, NO token (reloaded)", &Run{placement: "vm", cwd: "/repo", logsRoot: ckpt}, false},
		{"vm token + cwd, NO checkpoint", &Run{placement: "vm", cwd: "/repo", token: "t", logsRoot: t.TempDir()}, false},
		{"vm checkpoint + token, NO cwd", &Run{placement: "vm", token: "t", logsRoot: ckpt}, false},
		{"local checkpoint + cwd + token", &Run{placement: "local", cwd: "/repo", token: "t", logsRoot: ckpt}, true},
	}
	for _, c := range cases {
		if got := c.run.resumable(); got != c.want {
			t.Errorf("%s: resumable = %v, want %v", c.name, got, c.want)
		}
	}
}

// The run summary surfaces resumable=true for a failed VM run with a host-side
// checkpoint, so the UI's re-run-from-failure button lights up for it (the
// button is gated on this bit; no new UI needed for launcher-backed revive).
func TestSummary_ResumableForFailedVMRun(t *testing.T) {
	ckpt := writeCheckpoint(t, `{"context":{}}`)
	run := &Run{
		placement: "vm", cwd: "/repo", token: "t", logsRoot: ckpt,
		status:    RunFailed,
		questions: map[string]*pendingQuestion{},
	}
	if got, _ := run.Summary()["resumable"].(bool); !got {
		t.Fatal("summary resumable = false for a failed vm run with a checkpoint; re-run button would stay dark")
	}
}

// A run cancelled while wedged mid-flight — with a host-side checkpoint and the
// launch inputs still present — is exactly what an operator revives, so
// prepareRestart must accept status cancelled, not only failed.
func TestPrepareRestart_AllowsCancelledWithCheckpoint(t *testing.T) {
	ckpt := writeCheckpoint(t, `{"context":{}}`)
	run := &Run{
		ID: "c1", placement: "vm", cwd: "/repo", token: "t", logsRoot: ckpt,
		status:      RunCancelled,
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
	}
	if !run.prepareRestart() {
		t.Fatal("cancelled run with a checkpoint must be revivable")
	}
	if run.status != RunQueued {
		t.Fatalf("status = %v, want queued after prepareRestart", run.status)
	}
}

// A completed run has nothing to resume, cancelled or not: prepareRestart is a
// no-op for any non-failed, non-cancelled status.
func TestPrepareRestart_RejectsCompleted(t *testing.T) {
	ckpt := writeCheckpoint(t, `{"context":{}}`)
	run := &Run{
		ID: "c2", placement: "vm", cwd: "/repo", token: "t", logsRoot: ckpt,
		status:      RunCompleted,
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
	}
	if run.prepareRestart() {
		t.Fatal("completed run must not be revivable")
	}
}

// prepareRestart rotates the on-disk events.jsonl aside for launcher-backed
// runs too (not just direct): the daemon owns events.jsonl for a vm run
// (child runs --no-event-log), so the resumed timeline must start from a clean
// file with the failed attempt's log preserved under _restart_N.
func TestPrepareRestart_RotatesEventLogForVM(t *testing.T) {
	ckpt := writeCheckpoint(t, `{"context":{}}`)
	if err := os.WriteFile(filepath.Join(ckpt, "events.jsonl"), []byte("{\"seq\":1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := &Run{
		ID: "v1", placement: "vm", cwd: "/repo", token: "t", logsRoot: ckpt,
		status:      RunFailed,
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
	}
	if !run.prepareRestart() {
		t.Fatal("vm run with checkpoint must be revivable")
	}
	if _, err := os.Stat(filepath.Join(ckpt, "events.jsonl")); !os.IsNotExist(err) {
		t.Errorf("events.jsonl not rotated aside (err %v)", err)
	}
	if _, err := os.Stat(filepath.Join(ckpt, "_restart_1", "events.jsonl")); err != nil {
		t.Errorf("failed attempt's events.jsonl not preserved under _restart_1: %v", err)
	}
}
