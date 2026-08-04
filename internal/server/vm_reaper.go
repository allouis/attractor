package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// vmReaper garbage-collects persisted VMs whose runs finished more than
// `retention` ago. A completed VM is left running for post-run inspection
// (docs/nix-vm-runner-spec.md V17); the reaper eventually kills its QEMU
// process and removes its disk + job dir so VMs don't accumulate forever.
type vmReaper struct {
	vmDir     string
	retention time.Duration
	interval  time.Duration
	now       func() time.Time
	kill      func(pid int) error              // injectable for tests
	forget    func(repoDir, name string) error // injectable for tests
}

// StartVMReaper launches a background reaper GC-ing VMs older than
// retention under vmDir, and returns a channel that stops it when closed.
func StartVMReaper(vmDir string, retention time.Duration) chan<- struct{} {
	stop := make(chan struct{})
	go newVMReaper(vmDir, retention).Run(stop)
	return stop
}

// newVMReaper returns a reaper for vmDir with the given retention.
func newVMReaper(vmDir string, retention time.Duration) *vmReaper {
	return &vmReaper{
		vmDir:     vmDir,
		retention: retention,
		interval:  time.Hour,
		now:       time.Now,
		kill:      killPID,
		forget:    forgetWorkspace,
	}
}

// killPID sends SIGTERM to a process, ignoring "already gone".
func killPID(pid int) error {
	err := syscall.Kill(pid, syscall.SIGTERM)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

// Run sweeps immediately then every interval until stop is closed.
func (r *vmReaper) Run(stop <-chan struct{}) {
	r.sweep()
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			r.sweep()
		}
	}
}

// sweep reaps every VM older than retention and returns the run ids it
// reaped. A record whose dir is already gone is skipped.
func (r *vmReaper) sweep() []string {
	entries, err := os.ReadDir(r.vmDir)
	if err != nil {
		return nil
	}
	var reaped []string
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		dir := filepath.Join(r.vmDir, ent.Name())
		data, err := os.ReadFile(filepath.Join(dir, "vm.json"))
		if err != nil {
			continue // not a recorded VM (never completed / already reaped)
		}
		var rec vmRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		if r.now().Sub(rec.StartedAt) < r.retention {
			continue
		}
		if rec.Pid > 0 {
			_ = r.kill(rec.Pid)
		}
		// Kill every per-run virtiofsd serving a mount (workspace, jj store),
		// else a daemon leaks per reaped VM (spec W1/W2). VfsdPid is the
		// legacy single-daemon field kept for records predating multi-mount.
		for _, pid := range rec.VfsdPids {
			if pid > 0 {
				_ = r.kill(pid)
			}
		}
		if rec.VfsdPid > 0 {
			_ = r.kill(rec.VfsdPid)
		}
		// Forget the per-run jj workspace before removing its dir, else the
		// host repo accumulates stale run-<id> workspaces pointing at
		// deleted dirs (spec W1). Legacy records without repo/workspace are
		// pre-workspace VMs — nothing to forget.
		if rec.RepoDir != "" && rec.Workspace != "" && r.forget != nil {
			_ = r.forget(rec.RepoDir, rec.Workspace)
		}
		_ = os.RemoveAll(dir)
		reaped = append(reaped, rec.RunID)
	}
	return reaped
}
