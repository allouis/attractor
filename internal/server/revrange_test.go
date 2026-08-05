package server

import (
	"os"
	"os/exec"
	"testing"
)

// TestJJChangeID reads the working-copy change id from a real jj repo. It is
// the base/tip probe the daemon stamps around a host run so the Diff panel can
// serve `jj diff --from base --to tip` (ui-tailwind-spec T9c).
func TestJJChangeID(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t)
	id, err := jjChangeID(repo)
	if err != nil {
		t.Fatalf("jjChangeID: %v", err)
	}
	if id == "" {
		t.Fatal("jjChangeID returned empty change id")
	}

	// A non-jj directory yields an error, not a change id — the caller treats
	// that as "no range" (VM / non-repo run) and records nothing.
	if _, err := jjChangeID(t.TempDir()); err == nil {
		t.Fatal("jjChangeID on a non-jj dir should error")
	}
}

// TestRevRangePersistsAcrossReload proves the recorded jj rev range survives a
// daemon restart: a manifest carrying rev_base/rev_tip reloads into the run so
// the Diff endpoint still knows the range after the process that ran it is
// gone (T9c — the range is stamped on the run, not re-derived on read).
func TestRevRangePersistsAcrossReload(t *testing.T) {
	base := t.TempDir()
	writeManifestDir(t, base, "run1", Manifest{
		ID:      "run1",
		Status:  RunCompleted,
		RevBase: "basechange",
		RevTip:  "tipchange",
	})

	r := newRunRegistry(base)
	run, ok := r.Get("run1")
	if !ok {
		t.Fatal("run1 not reloaded")
	}
	if got := run.RevBase(); got != "basechange" {
		t.Errorf("RevBase = %q, want basechange", got)
	}
	if got := run.RevTip(); got != "tipchange" {
		t.Errorf("RevTip = %q, want tipchange", got)
	}
}

// TestRecordRevRangeSkipsVM proves a VM-placed run records no host jj range:
// the run executes in the guest, so the daemon's view of run.cwd is meaningless
// and must stay empty (spec: VM runs stay "no diff" until results-export).
func TestRecordRevRangeSkipsVM(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t)
	run := &Run{ID: "vm1", cwd: repo, placement: "vm", logsRoot: os.DevNull}
	run.recordRevBase()
	run.recordRevTip()
	if run.RevBase() != "" || run.RevTip() != "" {
		t.Errorf("vm run recorded a rev range: base=%q tip=%q", run.RevBase(), run.RevTip())
	}
}
