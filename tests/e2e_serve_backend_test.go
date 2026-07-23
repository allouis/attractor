package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/cli"
	"github.com/fabro/attractor/internal/server"
)

// TestServe_RoutesNodesThroughProviderConfig is the server-side mirror of
// TestCLI_RoutesNodesThroughProviderConfig: the handler factory `serve`
// wires from the machine-local provider config routes each codergen node
// to its backend, and llm_model reaches the agent via model_env.
func TestServe_RoutesNodesThroughProviderConfig(t *testing.T) {
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

	factory, err := cli.ServeHandlerFactory()
	must(t, err)
	logsRoot := t.TempDir()
	srv := server.New(server.Config{Addr: "127.0.0.1:0", LogsRoot: logsRoot, MakeHandlers: factory})
	must(t, srv.Start())
	t.Cleanup(func() { _ = srv.Close() })

	dot := `digraph route {
		goal = "x"
		start  [shape=Mdiamond]
		plan   [shape=box, prompt="plan it", llm_provider="anthropic", llm_model="opus-routed"]
		review [shape=box, prompt="review it", llm_provider="openai", llm_model="gpt-routed"]
		done   [shape=Msquare]
		start -> plan -> review -> done
	}`
	id := submitJSON(t, srv, map[string]any{"dot": dot})
	waitForRunStatus(t, srv, id, "completed")

	planResp, err := os.ReadFile(filepath.Join(logsRoot, id, "plan", "response.md"))
	must(t, err)
	if !strings.Contains(string(planResp), "model=opus-routed") {
		t.Fatalf("plan node not routed to anthropic with its model: %q", planResp)
	}
	reviewResp, err := os.ReadFile(filepath.Join(logsRoot, id, "review", "response.md"))
	must(t, err)
	if !strings.Contains(string(reviewResp), "model=gpt-routed") {
		t.Fatalf("review node not routed to openai with its model: %q", reviewResp)
	}
}
