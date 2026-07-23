package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/cli"
)

// chdir switches to dir for the duration of the test, restoring the
// previous working directory on cleanup. (Hand-rolled rather than
// t.Chdir so the module can stay at go 1.23.)
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	must(t, err)
	must(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// TestCLI_RoutesNodesThroughProviderConfig runs a pipeline with no
// --backend flag: each codergen node is routed to a backend via
// ./.attractor/config.toml, and each node's llm_model reaches its agent
// through the provider's model_env.
func TestCLI_RoutesNodesThroughProviderConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	chdir(t, work)

	agent := fakeACPCommand(t)
	writeConfig(t, filepath.Join(work, ".attractor", "config.toml"), `
default_provider = "anthropic"

[providers.anthropic]
backend   = "acp"
command   = "`+agent+`"
model_env = "FAKE_MODEL"

[providers.openai]
backend   = "acp"
command   = "`+agent+`"
model_env = "FAKE_MODEL"
`)

	pipeline := filepath.Join(work, "route.dot")
	must(t, os.WriteFile(pipeline, []byte(`digraph route {
		goal = "x"
		start  [shape=Mdiamond]
		plan   [shape=box, prompt="plan it", llm_provider="anthropic", llm_model="opus-routed"]
		review [shape=box, prompt="review it", llm_provider="openai", llm_model="gpt-routed"]
		done   [shape=Msquare]
		start -> plan -> review -> done
	}`), 0o644))

	logsRoot := t.TempDir()
	err := cli.Run([]string{"--logs", logsRoot, pipeline})
	must(t, err)

	planResp, err := os.ReadFile(filepath.Join(logsRoot, "plan", "response.md"))
	must(t, err)
	if !strings.Contains(string(planResp), "model=opus-routed") {
		t.Fatalf("plan node should be routed to anthropic with its model, got %q", planResp)
	}
	reviewResp, err := os.ReadFile(filepath.Join(logsRoot, "review", "response.md"))
	must(t, err)
	if !strings.Contains(string(reviewResp), "model=gpt-routed") {
		t.Fatalf("review node should be routed to openai with its model, got %q", reviewResp)
	}
}

// TestCLI_ExplicitBackendOverridesConfig: passing --backend keeps the
// run-wide single-backend debugging path, ignoring provider config.
func TestCLI_ExplicitBackendOverridesConfig(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	t.Setenv("HOME", home)
	chdir(t, work)

	// Config would route to a nonexistent agent; --backend simulation
	// must win so the run still succeeds without spawning it.
	writeConfig(t, filepath.Join(work, ".attractor", "config.toml"), `
default_provider = "anthropic"
[providers.anthropic]
backend = "acp"
command = "/nonexistent/agent"
`)
	pipeline := filepath.Join(work, "route.dot")
	must(t, os.WriteFile(pipeline, []byte(`digraph route {
		start [shape=Mdiamond]
		plan  [shape=box, prompt="plan it", llm_provider="anthropic"]
		done  [shape=Msquare]
		start -> plan -> done
	}`), 0o644))

	logsRoot := t.TempDir()
	err := cli.Run([]string{"--backend", "simulation", "--logs", logsRoot, pipeline})
	must(t, err)

	resp, err := os.ReadFile(filepath.Join(logsRoot, "plan", "response.md"))
	must(t, err)
	if !strings.Contains(string(resp), "[simulated]") {
		t.Fatalf("--backend simulation should override config routing, got %q", resp)
	}
}
