package attractor_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// newTestServerWithLogs is like newTestServer but returns the logs root
// so tests can inspect per-run stage artefacts.
func newTestServerWithLogs(t *testing.T, factory server.HandlerFactory) (*server.Server, string) {
	t.Helper()
	logsRoot := t.TempDir()
	srv := server.New(server.Config{
		Addr:         "127.0.0.1:0",
		LogsRoot:     logsRoot,
		MakeHandlers: factory,
	})
	must(t, srv.Start())
	t.Cleanup(func() { _ = srv.Close() })
	return srv, logsRoot
}

// submitJSON posts a JSON pipeline submission and returns the run id.
func submitJSON(t *testing.T, srv *server.Server, payload map[string]any) string {
	t.Helper()
	body, err := json.Marshal(payload)
	must(t, err)
	resp, err := http.Post(srv.URL()+"/pipelines", "application/json", bytes.NewReader(body))
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("submit failed: %d %s", resp.StatusCode, b)
	}
	var out struct{ ID string }
	must(t, json.NewDecoder(resp.Body).Decode(&out))
	return out.ID
}

// TestServe_SubmitJSONExpandsVars confirms the serve path runs the same
// VariableExpansion transform as the CLI: $vars from the payload land in
// the rendered prompt.
func TestServe_SubmitJSONExpandsVars(t *testing.T) {
	srv, logsRoot := newTestServerWithLogs(t, server.DefaultHandlers(handler.Codergen{}))
	dot := `digraph p {
		vars = "epic_id"
		start [shape=Mdiamond]
		work [prompt="Implement $epic_id"]
		done [shape=Msquare]
		start -> work -> done
	}`
	id := submitJSON(t, srv, map[string]any{
		"dot":  dot,
		"vars": map[string]string{"epic_id": "ABC-123"},
	})
	waitForRunStatus(t, srv, id, "completed")
	body, err := os.ReadFile(filepath.Join(logsRoot, id, "work", "prompt.md"))
	must(t, err)
	if !strings.Contains(string(body), "Implement ABC-123") {
		t.Fatalf("var not expanded into serve prompt: %q", body)
	}
}

// TestServe_SubmitResolvesFilePromptFromCwd confirms @file prompts are
// inlined relative to the submission cwd (base_dir defaults to cwd).
func TestServe_SubmitResolvesFilePromptFromCwd(t *testing.T) {
	repo := t.TempDir()
	must(t, os.WriteFile(filepath.Join(repo, "plan.md"), []byte("Plan: $task"), 0o644))
	srv, logsRoot := newTestServerWithLogs(t, server.DefaultHandlers(handler.Codergen{}))
	dot := `digraph p {
		vars = "task"
		start [shape=Mdiamond]
		plan [prompt="@plan.md"]
		done [shape=Msquare]
		start -> plan -> done
	}`
	id := submitJSON(t, srv, map[string]any{
		"dot":  dot,
		"vars": map[string]string{"task": "ship it"},
		"cwd":  repo,
	})
	waitForRunStatus(t, srv, id, "completed")
	body, err := os.ReadFile(filepath.Join(logsRoot, id, "plan", "prompt.md"))
	must(t, err)
	if !strings.Contains(string(body), "Plan: ship it") {
		t.Fatalf("@file + cwd base_dir not resolved on serve: %q", body)
	}
}

// TestServe_SubmitCwdSetsGraphDefault confirms the payload cwd becomes
// the graph-level cwd default: a tool node runs in the submission cwd.
func TestServe_SubmitCwdSetsGraphDefault(t *testing.T) {
	repo := t.TempDir()
	must(t, os.WriteFile(filepath.Join(repo, "marker.txt"), []byte("hi"), 0o644))
	srv, logsRoot := newTestServerWithLogs(t, server.DefaultHandlers(handler.Codergen{}))
	dot := `digraph p {
		start [shape=Mdiamond]
		probe [shape=parallelogram, tool_command="pwd && cat marker.txt"]
		done [shape=Msquare]
		start -> probe -> done
	}`
	id := submitJSON(t, srv, map[string]any{"dot": dot, "cwd": repo})
	waitForRunStatus(t, srv, id, "completed")
	body, err := os.ReadFile(filepath.Join(logsRoot, id, "probe", "stdout.txt"))
	must(t, err)
	if !strings.Contains(string(body), repo) || !strings.Contains(string(body), "hi") {
		t.Fatalf("payload cwd did not become graph cwd default: %q", body)
	}
}

// TestServe_SubmitMissingDeclaredVar rejects a submission whose declared
// vars are not all supplied, matching the CLI's hard error.
func TestServe_SubmitMissingDeclaredVar(t *testing.T) {
	srv, _ := newTestServerWithLogs(t, server.DefaultHandlers(handler.Codergen{}))
	dot := `digraph p {
		vars = "epic_id"
		start [shape=Mdiamond]
		work [prompt="Implement $epic_id"]
		done [shape=Msquare]
		start -> work -> done
	}`
	body, _ := json.Marshal(map[string]any{"dot": dot})
	resp, err := http.Post(srv.URL()+"/pipelines", "application/json", bytes.NewReader(body))
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("missing declared var: status=%d, want 422", resp.StatusCode)
	}
}
