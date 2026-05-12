package attractor_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/cli"
)

// TestCLI_RunsWithFakeClaudeOnPATH verifies that `attractor run`
// auto-detects a `claude` binary on PATH (here a fake stream-json
// emitter), wires the Claude backend, and produces a real response in
// response.md rather than the simulation-mode placeholder.
func TestCLI_RunsWithFakeClaudeOnPATH(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}

	// Build a directory containing a fake `claude` binary and prepend it
	// to PATH so the CLI's auto-detect picks it up.
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "claude")
	script := "#!" + bashPath(t) + "\n" +
		`echo '{"type":"system","subtype":"init"}'` + "\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"hello from claude"}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","result":"hello from claude","is_error":false}'` + "\n"
	must(t, os.WriteFile(claudePath, []byte(script), 0o755))

	// Fake an auth credential so AvailableAuth() returns true.
	home := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	must(t, os.WriteFile(filepath.Join(home, ".claude", "credentials.json"), []byte("{}"), 0o600))

	// Patch env for the subprocess and the test.
	oldPath := os.Getenv("PATH")
	oldHome := os.Getenv("HOME")
	must(t, os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath))
	must(t, os.Setenv("HOME", home))
	t.Cleanup(func() {
		_ = os.Setenv("PATH", oldPath)
		_ = os.Setenv("HOME", oldHome)
	})

	logsRoot := t.TempDir()
	err := cli.Run([]string{
		"--backend", "auto",
		"--logs", logsRoot,
		"../testdata/pipelines/smoke.dot",
	})
	must(t, err)

	// plan stage should have a real response (not simulation placeholder).
	resp, err := os.ReadFile(filepath.Join(logsRoot, "plan", "response.md"))
	must(t, err)
	if !strings.Contains(string(resp), "hello from claude") {
		t.Fatalf("plan response.md missing real reply: %q", resp)
	}
}

func TestCLI_FallsBackToSimulationWithoutClaude(t *testing.T) {
	// Hide claude from PATH and remove HOME-based credentials.
	dir := t.TempDir()
	oldPath := os.Getenv("PATH")
	oldHome := os.Getenv("HOME")
	must(t, os.Setenv("PATH", dir))
	must(t, os.Setenv("HOME", dir))
	must(t, os.Unsetenv("ANTHROPIC_API_KEY"))
	t.Cleanup(func() {
		_ = os.Setenv("PATH", oldPath)
		_ = os.Setenv("HOME", oldHome)
	})

	logsRoot := t.TempDir()
	err := cli.Run([]string{
		"--backend", "auto",
		"--logs", logsRoot,
		"../testdata/pipelines/smoke.dot",
	})
	must(t, err)

	resp, err := os.ReadFile(filepath.Join(logsRoot, "plan", "response.md"))
	must(t, err)
	if !strings.Contains(string(resp), "[simulated]") {
		t.Fatalf("expected simulation placeholder, got %q", resp)
	}
}

func TestCLI_ExplicitSimulationFlag(t *testing.T) {
	logsRoot := t.TempDir()
	err := cli.Run([]string{
		"--backend", "simulation",
		"--logs", logsRoot,
		"../testdata/pipelines/smoke.dot",
	})
	must(t, err)
	resp, err := os.ReadFile(filepath.Join(logsRoot, "plan", "response.md"))
	must(t, err)
	if !strings.Contains(string(resp), "[simulated]") {
		t.Fatalf("--backend simulation should skip claude lookup: %q", resp)
	}
}
