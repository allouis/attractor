package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/cli"
)

func TestCLI_ValidateGoodPipeline(t *testing.T) {
	if err := cli.Validate([]string{"../testdata/pipelines/smoke.dot"}); err != nil {
		t.Fatalf("validate good pipeline failed: %v", err)
	}
}

func TestCLI_ValidateBadPipeline(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "bad.dot")
	must(t, os.WriteFile(path, []byte(`digraph bad { a [prompt="x"] }`), 0o644))
	err := cli.Validate([]string{path})
	if err == nil {
		t.Fatal("expected validate to fail on pipeline without start/exit")
	}
	if !strings.Contains(err.Error(), "start_node") || !strings.Contains(err.Error(), "terminal_node") {
		t.Fatalf("error should mention failing rules: %v", err)
	}
}

func TestCLI_RunSimulationMode(t *testing.T) {
	logsRoot := t.TempDir()
	args := []string{"--logs", logsRoot, "../testdata/pipelines/smoke.dot"}
	if err := cli.Run(args); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	// Each codergen node produced status.json + prompt.md + response.md
	// even in simulation mode.
	for _, id := range []string{"plan", "implement", "review"} {
		for _, f := range []string{"prompt.md", "response.md", "status.json"} {
			path := filepath.Join(logsRoot, id, f)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("missing %s: %v", path, err)
			}
		}
	}
	// $goal expanded into the plan prompt.
	prompt, err := os.ReadFile(filepath.Join(logsRoot, "plan", "prompt.md"))
	must(t, err)
	if !strings.Contains(string(prompt), "Create a hello world Python script") {
		t.Fatalf("plan prompt missing $goal expansion: %q", prompt)
	}
}

func TestCLI_RunMissingFileReturnsError(t *testing.T) {
	err := cli.Run([]string{"./no-such-file.dot"})
	if err == nil {
		t.Fatal("expected error on missing file")
	}
}
