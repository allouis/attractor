package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVMRunnerNixSharesRunLogs pins the nix-only half of P5c against the Go
// side (ui-run-view-v3 P5c): the module must expand ATTRACTOR_LOGS (the source
// vmEnv sets) into a /mnt/runlogs share, and the runner must point the child's
// --logs there with --no-event-log so the daemon owns events.jsonl. A content
// check, since a guest cannot be booted here.
func TestVMRunnerNixSharesRunLogs(t *testing.T) {
	// server tests run with cwd = internal/server; the module is two dirs up.
	data, err := os.ReadFile(filepath.Join("..", "..", "nix", "vm-runner.nix"))
	if err != nil {
		t.Fatalf("read vm-runner.nix: %v", err)
	}
	nix := string(data)
	for _, want := range []string{
		`"$ATTRACTOR_LOGS"`,   // share source, set by vmEnv
		"/mnt/runlogs",        // guest mount target
		"--logs /mnt/runlogs", // child logs root points at the share
		"--no-event-log",      // daemon stays the single events.jsonl writer
		"mnt-runlogs.mount",   // runner waits for the mount
	} {
		if !strings.Contains(nix, want) {
			t.Errorf("nix/vm-runner.nix missing %q — P5c runlogs share not wired", want)
		}
	}
}
