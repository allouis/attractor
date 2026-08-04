package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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

// A persisted VM carries a per-run virtiofsd (serving the workspace mount);
// the reaper must kill it alongside the qemu process or the daemon leaks
// (spec W1). A record with no vfsd_pid (legacy / never recorded) kills only
// the qemu pid.
func TestReaperKillsVirtiofsd(t *testing.T) {
	vmDir := t.TempDir()
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	dir := filepath.Join(vmDir, "old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(vmRecord{RunID: "old", Pid: 4242, VfsdPid: 9191, Dir: dir, StartedAt: now.Add(-80 * time.Hour)})
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
	if !slices.Equal(killed, []int{4242, 9191}) {
		t.Fatalf("killed = %v, want both qemu (4242) and virtiofsd (9191) pids", killed)
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
