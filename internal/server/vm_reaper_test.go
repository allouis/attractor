package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeVMRecord(t *testing.T, vmDir, runID string, pid int, started time.Time) string {
	t.Helper()
	dir := filepath.Join(vmDir, runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(vmRecord{RunID: runID, Pid: pid, Dir: dir, StartedAt: started})
	if err := os.WriteFile(filepath.Join(dir, "vm.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReaperReapsOnlyExpiredVMs(t *testing.T) {
	vmDir := t.TempDir()
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	oldDir := writeVMRecord(t, vmDir, "old", 4242, now.Add(-80*time.Hour))
	freshDir := writeVMRecord(t, vmDir, "fresh", 4343, now.Add(-1*time.Hour))

	var killed []int
	r := &vmReaper{
		vmDir:     vmDir,
		retention: 72 * time.Hour,
		now:       func() time.Time { return now },
		kill:      func(pid int) error { killed = append(killed, pid); return nil },
	}
	reaped := r.sweep()

	if !slices.Equal(reaped, []string{"old"}) {
		t.Fatalf("reaped = %v, want [old]", reaped)
	}
	if !slices.Equal(killed, []int{4242}) {
		t.Fatalf("killed = %v, want [4242]", killed)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old VM dir not removed")
	}
	if _, err := os.Stat(freshDir); err != nil {
		t.Fatalf("fresh VM dir wrongly removed: %v", err)
	}
}

// GS dropped virtiofs, but a VM in flight ACROSS the upgrade left a vm.json
// carrying vfsd_pid/vfsd_pids for daemons the new launcher no longer records.
// The reaper must still kill those legacy pids, else the daemons orphan
// permanently (they outlive their qemu, which the reaper does kill). Kept as
// a back-compat shim for one retention window; new records carry no vfsd pids.
func TestReaperKillsLegacyVirtiofsdPids(t *testing.T) {
	vmDir := t.TempDir()
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	dir := filepath.Join(vmDir, "old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A pre-GS record: qemu pid + both the multi-mount (vfsd_pids) and the
	// legacy single-daemon (vfsd_pid) shapes.
	data, _ := json.Marshal(vmRecord{RunID: "old", Pid: 4242, VfsdPids: []int{9191, 9292}, VfsdPid: 9393, Dir: dir, StartedAt: now.Add(-80 * time.Hour)})
	if err := os.WriteFile(filepath.Join(dir, "vm.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	var killed []int
	r := &vmReaper{
		vmDir:     vmDir,
		retention: 72 * time.Hour,
		now:       func() time.Time { return now },
		kill:      func(pid int) error { killed = append(killed, pid); return nil },
	}
	r.sweep()

	slices.Sort(killed)
	if !slices.Equal(killed, []int{4242, 9191, 9292, 9393}) {
		t.Fatalf("killed = %v, want qemu + all legacy virtiofsd pids; orphaned daemons leak on upgrade", killed)
	}
}

// Reaping a VM must `jj workspace forget` the per-run workspace, not just
// os.RemoveAll its dir — otherwise the host repo accumulates stale
// run-<id> workspaces pointing at deleted dirs, growing unbounded (B1).
func TestReaperForgetsJJWorkspace(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := t.TempDir()
	if out, err := exec.Command("jj", "git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("jj git init: %v\n%s", err, out)
	}
	vmDir := t.TempDir()
	dir := filepath.Join(vmDir, "old")
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := materializeWorkspace(repo, work, "run-old", "@"); err != nil {
		t.Fatalf("materializeWorkspace: %v", err)
	}
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	data, _ := json.Marshal(vmRecord{RunID: "old", Dir: dir, RepoDir: repo, Workspace: "run-old", StartedAt: now.Add(-80 * time.Hour)})
	if err := os.WriteFile(filepath.Join(dir, "vm.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	r := newVMReaper(vmDir, 72*time.Hour)
	r.now = func() time.Time { return now }
	r.kill = func(int) error { return nil }
	r.sweep()

	out, err := exec.Command("jj", "-R", repo, "workspace", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("workspace list: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "run-old") {
		t.Fatalf("stale workspace not forgotten:\n%s", out)
	}
}

// A dir without vm.json (a run whose VM never completed / already reaped)
// is skipped, not touched.
func TestReaperSkipsUnrecordedDirs(t *testing.T) {
	vmDir := t.TempDir()
	bare := filepath.Join(vmDir, "bare")
	os.MkdirAll(bare, 0o755)
	r := &vmReaper{vmDir: vmDir, retention: time.Hour, now: time.Now, kill: func(int) error { return nil }}
	if got := r.sweep(); got != nil {
		t.Fatalf("reaped %v, want none", got)
	}
	if _, err := os.Stat(bare); err != nil {
		t.Fatalf("bare dir removed: %v", err)
	}
}
