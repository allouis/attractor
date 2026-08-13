package attractor_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/cli"
)

// putFakeClaudeOnPATH builds a directory containing a fake `claude`
// binary (a stream-json emitter) and prepends it to PATH.
func putFakeClaudeOnPATH(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	dir := t.TempDir()
	claudePath := filepath.Join(dir, "claude")
	script := "#!" + bashPath(t) + "\n" +
		`echo '{"type":"system","subtype":"init"}'` + "\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"hello from claude"}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","result":"hello from claude","is_error":false}'` + "\n"
	must(t, os.WriteFile(claudePath, []byte(script), 0o755))

	oldPath := os.Getenv("PATH")
	must(t, os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath))
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
}

// TestCLI_ExplicitClaudeBackend verifies that `attractor run --backend
// claude` wires the Claude backend and produces a real response in
// response.md rather than the simulation-mode placeholder.
func TestCLI_ExplicitClaudeBackend(t *testing.T) {
	putFakeClaudeOnPATH(t)

	logsRoot := t.TempDir()
	err := cli.Run([]string{
		"--backend", "claude",
		"--logs", logsRoot,
		"../testdata/pipelines/smoke.dot",
	})
	must(t, err)

	resp, err := os.ReadFile(spanPath(t, logsRoot, "plan", "response.md"))
	must(t, err)
	if !strings.Contains(string(resp), "hello from claude") {
		t.Fatalf("plan response.md missing real reply: %q", resp)
	}
}

// TestCLI_DefaultBackendIsSimulation verifies that omitting --backend
// runs in simulation mode even when a usable claude binary sits on
// PATH — backend selection is always explicit.
func TestCLI_DefaultBackendIsSimulation(t *testing.T) {
	putFakeClaudeOnPATH(t)

	logsRoot := t.TempDir()
	err := cli.Run([]string{
		"--logs", logsRoot,
		"../testdata/pipelines/smoke.dot",
	})
	must(t, err)

	resp, err := os.ReadFile(spanPath(t, logsRoot, "plan", "response.md"))
	must(t, err)
	if !strings.Contains(string(resp), "[simulated]") {
		t.Fatalf("default backend should be simulation, got %q", resp)
	}
}

// TestCLI_UnknownBackendErrors verifies that an unrecognised --backend
// value (including the removed `auto`) is a hard error naming the
// valid choices.
func TestCLI_UnknownBackendErrors(t *testing.T) {
	logsRoot := t.TempDir()
	err := cli.Run([]string{
		"--backend", "auto",
		"--logs", logsRoot,
		"../testdata/pipelines/smoke.dot",
	})
	if err == nil {
		t.Fatal("expected error for --backend auto")
	}
	if !strings.Contains(err.Error(), "unknown backend") {
		t.Fatalf("error should name the problem, got %q", err)
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
	resp, err := os.ReadFile(spanPath(t, logsRoot, "plan", "response.md"))
	must(t, err)
	if !strings.Contains(string(resp), "[simulated]") {
		t.Fatalf("--backend simulation should skip claude lookup: %q", resp)
	}
}
