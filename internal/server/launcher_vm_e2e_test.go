package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// jjRun runs a jj command against repo, failing the test on error.
func jjRun(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("jj", append([]string{"-R", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("jj %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// TestVMWorkspaceAcceptance is W2's pivotal, authoritative gate: it boots a
// real vm-runner VM against a per-run jj workspace delivered over virtiofs
// and asserts the two failure surfaces the spec exists to fix now work —
// `pnpm install` with its SQLite store on the mount (no `disk I/O error`),
// and in-guest `jj` committing into the shared HOST store (visible in the
// host's `jj log`, no export step). See docs/vm-workspace-spec.md W2 and
// testdata/pipelines/vm-workspace-acceptance.dot.
//
// Empirical + slow (a NixOS VM boot + pnpm install under TCG), so it is
// gated behind ATTRACTOR_VM_E2E and skipped in the normal `go test` gate —
// mirroring the ATTRACTOR_REAL_CLAUDE pattern. Run it on the box:
//
//	nix build .#vm-runner
//	ATTRACTOR_VM_E2E=1 nix develop -c go test ./internal/server/ \
//	    -run TestVMWorkspaceAcceptance -count=1 -timeout 40m
func TestVMWorkspaceAcceptance(t *testing.T) {
	if os.Getenv("ATTRACTOR_VM_E2E") == "" {
		t.Skip("set ATTRACTOR_VM_E2E=1 to boot a real vm-runner VM (slow; needs qemu + virtiofsd; W2 acceptance)")
	}
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	if _, err := exec.LookPath("virtiofsd"); err != nil {
		t.Skip("virtiofsd not on PATH (enter `nix develop`)")
	}
	// The run-nixos-vm boot script from `nix build .#vm-runner`. Default to
	// the ./result symlink; override with ATTRACTOR_VM_RUNNER.
	runner := os.Getenv("ATTRACTOR_VM_RUNNER")
	if runner == "" {
		runner = "../../result/bin/run-nixos-vm"
	}
	if _, err := os.Stat(runner); err != nil {
		t.Skipf("vm-runner boot script not found (%v); run `nix build .#vm-runner` or set ATTRACTOR_VM_RUNNER", err)
	}

	// Fixture: a jj-colocated repo with a package.json (a tracked file, so it
	// materialises into the workspace) whose one tiny zero-dep package forces
	// pnpm to open/write its SQLite store index.
	repo := t.TempDir()
	if out, err := exec.Command("jj", "git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("jj git init: %v\n%s", err, out)
	}
	writeFixture(t, repo, "package.json", `{
  "name": "vm-workspace-acceptance",
  "version": "1.0.0",
  "private": true,
  "dependencies": { "is-number": "7.0.0" }
}
`)
	writeFixture(t, repo, ".gitignore", "node_modules\n.pnpm-store\n")
	jjRun(t, repo, "describe", "-m", "acceptance fixture")

	source, err := os.ReadFile("../../testdata/pipelines/vm-workspace-acceptance.dot")
	if err != nil {
		t.Fatal(err)
	}

	// A live daemon so the guest can phone its events home over user-net
	// (10.0.2.2 -> host loopback); Launch blocks until the run reports
	// terminal.
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir()})
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	defer srv.Close()

	run := srv.registry.NewRun(string(source), nil, nil, repo, nil, "", "", repo, nil)
	run.cwd = repo

	vmDir := t.TempDir()
	l := NewVMLauncher(runner, vmDir)

	done := make(chan error, 1)
	go func() { done <- l.Launch(run, srv.reportURL()) }()

	timeout := 40 * time.Minute
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Launch: %v", err)
		}
	case <-time.After(timeout):
		t.Fatalf("VM run did not finish within %s (see %s)", timeout, filepath.Join(vmDir, run.ID))
	}

	console, _ := os.ReadFile(filepath.Join(vmDir, run.ID, "vm-console.log"))
	if run.Status() != RunCompleted {
		t.Fatalf("run status = %s, want completed; console:\n%s", run.Status(), tail(string(console), 8000))
	}
	// The bug that started the spec: pnpm's SQLite store index on the share.
	if strings.Contains(string(console), "disk I/O error") {
		t.Fatalf("pnpm hit `disk I/O error` — SQLite over virtiofs is broken; invoke the guest-local-clone fallback (W2)")
	}
	// In-guest jj committed into the shared host store — visible on the host
	// with no export step (spec W2 "Result surfacing" precondition).
	log := jjRun(t, repo, "log", "--no-graph", "--ignore-working-copy", "-r", "all()", "-T", `description ++ "\n"`)
	if !strings.Contains(log, "vm-workspace-acceptance ran") {
		oplog, _ := exec.Command("jj", "-R", repo, "op", "log", "--no-graph", "--ignore-working-copy", "-T", `description ++ "\n"`).CombinedOutput()
		t.Fatalf("guest jj commit not visible in host jj log — store write did not land:\nlog:\n%s\nop log:\n%s\nconsole:\n%s", log, oplog, tail(string(console), 6000))
	}
}

// writeFixture writes a fixture file into repo.
func writeFixture(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// tail returns the last n bytes of s, prefixed with an ellipsis when clipped.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
