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
	kill      func(pid int) error // injectable for tests
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
		// Kill the per-run virtiofsd serving the workspace mount too, else
		// the daemon leaks one per reaped VM (spec W1).
		if rec.VfsdPid > 0 {
			_ = r.kill(rec.VfsdPid)
		}
		_ = os.RemoveAll(dir)
		reaped = append(reaped, rec.RunID)
	}
	return reaped
}
