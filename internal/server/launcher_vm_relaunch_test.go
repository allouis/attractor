package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A restart re-enters Launch with the previous attempt's workspace still on
// disk and registered in the store. Launch must REUSE it rather than
// re-materialize — re-materializing resets the workspace to run.cwd@ current,
// dropping the attempt's committed work. Proven by a sentinel file placed in
// the existing workspace: a reuse keeps it, a re-materialize (fresh working
// copy from run.cwd) wipes it.
func TestLaunch_ReusesExistingWorkspace(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t)
	vmDir := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir()})
	run := srv.registry.NewRun("digraph{}", nil, nil, repo, nil, "", "", "", nil)
	run.cwd = repo

	// Pre-create the workspace as a prior attempt would have left it, then drop
	// an (untracked) sentinel standing in for the attempt's on-disk work.
	if err := os.MkdirAll(filepath.Join(vmDir, run.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(vmDir, run.ID, "work")
	if err := materializeWorkspace(repo, work, runWorkspaceName(run.ID)); err != nil {
		t.Fatalf("pre-materialize: %v", err)
	}
	sentinel := filepath.Join(work, "attempt-work.txt")
	if err := os.WriteFile(sentinel, []byte("committed work"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := vmLauncher{
		images: map[string]string{"default": "/nonexistent-runner"}, defaultImage: "default",
		vmDir: vmDir, guestHost: "10.0.2.2", pollInterval: time.Millisecond,
	}
	_ = l.Launch(run, "http://127.0.0.1:0") // boot fails (missing script); setup ran first

	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("existing workspace was re-materialized on relaunch (sentinel gone: %v); attempt work would be lost", err)
	}
}

// A restart must boot a FRESH qcow2: a crashed guest's disk may hold a
// half-written filesystem that would poison the resume. Launch deletes any
// stale vm.qcow2 before booting (run-nixos-vm otherwise reuses it).
func TestLaunch_ReplacesStaleQcow2(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t)
	vmDir := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir()})
	run := srv.registry.NewRun("digraph{}", nil, nil, repo, nil, "", "", "", nil)
	run.cwd = repo

	runDir := filepath.Join(vmDir, run.ID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	qcow2 := filepath.Join(runDir, "vm.qcow2")
	if err := os.WriteFile(qcow2, []byte("stale crashed-guest disk"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := vmLauncher{
		images: map[string]string{"default": "/nonexistent-runner"}, defaultImage: "default",
		vmDir: vmDir, guestHost: "10.0.2.2", pollInterval: time.Millisecond,
	}
	_ = l.Launch(run, "http://127.0.0.1:0") // boot fails, but the stale disk is removed first

	if data, err := os.ReadFile(qcow2); err == nil && string(data) == "stale crashed-guest disk" {
		t.Fatal("stale vm.qcow2 was not replaced before boot — a crashed-guest disk would poison the resume")
	}
}
