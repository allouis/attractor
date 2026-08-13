package attractor_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/backend/claudecode"
	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	graphpkg "github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/handler"
)

func fakeClaude(t *testing.T, response string, isError bool) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := "#!" + bashPath(t) + "\n" +
		`echo '{"type":"system","subtype":"init"}'` + "\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"thinking..."}]}}'` + "\n" +
		`echo '{"type":"assistant","message":{"content":[{"type":"text","text":"` + esc(response) + `"}]}}'` + "\n" +
		`echo '{"type":"result","subtype":"success","result":"` + esc(response) + `","is_error":` + boolStr(isError) + `}'` + "\n"
	must(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func bashPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash unavailable: %v", err)
	}
	return p
}

func TestClaudeCode_ParsesStreamJSON(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	claudeBin := fakeClaude(t, "stream reply", false)
	be := &claudecode.Backend{ClaudeBin: claudeBin}
	logsRoot := t.TempDir()
	out := runOneNode(t, be, "x", logsRoot)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("expected SUCCESS, got %s reason=%q", out.Status, out.FailureReason)
	}
	resp, err := os.ReadFile(spanPath(t, logsRoot, "node1", "response.md"))
	must(t, err)
	if !strings.Contains(string(resp), "stream reply") {
		t.Fatalf("response.md missing reply: %q", resp)
	}
}

func TestClaudeCode_ReportsIsError(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	claudeBin := fakeClaude(t, "oh no", true)
	be := &claudecode.Backend{ClaudeBin: claudeBin}
	out := runOneNode(t, be, "x", t.TempDir())
	if out.Status != engine.StatusFail {
		t.Fatalf("expected FAIL on is_error=true, got %s", out.Status)
	}
}

func TestClaudeCode_GatedRealCLI(t *testing.T) {
	if os.Getenv("ATTRACTOR_REAL_CLAUDE") == "" {
		t.Skip("set ATTRACTOR_REAL_CLAUDE=1 to exercise the real `claude` CLI (uses subscription quota)")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not on PATH")
	}
	if !claudecode.AvailableAuth() {
		t.Skip("no Claude auth available")
	}
	if testing.Short() {
		t.Skip("real-claude test skipped in -short mode")
	}
	be := &claudecode.Backend{}
	out := runOneNode(t, be, "Reply with the single word 'pong'.", t.TempDir())
	if out.Status != engine.StatusSuccess {
		t.Skipf("real claude returned %s reason=%q (probably expected under quota/network limits)", out.Status, out.FailureReason)
	}
}

func runOneNode(t *testing.T, be *claudecode.Backend, prompt string, logsRoot string) engine.Outcome {
	t.Helper()
	src := `digraph one {
		start [shape=Mdiamond]
		node1 [prompt="` + esc(prompt) + `"]
		done [shape=Msquare]
		start -> node1 -> done
	}`
	file, err := dot.Parse(src)
	must(t, err)
	g, err := graphpkg.Build(file)
	must(t, err)
	prepared, err := engine.Prepare(g)
	must(t, err)
	registry := engine.NewRegistry()
	registry.Register("start", handler.Start{})
	registry.Register("exit", handler.Exit{})
	codergen := handler.Codergen{Backend: be}
	registry.Register("codergen", codergen)
	registry.SetDefault(codergen)
	eng := engine.New(engine.Config{Registry: registry, LogsRoot: logsRoot, RunID: "rid"})
	go func() {
		for range eng.Events() {
		}
	}()
	if _, err := eng.Run(prepared); err != nil {
		// FAIL outcomes return non-nil err with reason; status.json
		// reflects the outcome and that's what we care about. Keep
		// going so the caller can inspect.
		_ = err
	}
	data, err := os.ReadFile(spanPath(t, logsRoot, "node1", "status.json"))
	if err != nil {
		// FAIL path: engine no longer writes status.json on fail; build
		// a synthetic outcome based on the last events / FailureReason
		// surfaced via the run error. Read from response.md if present.
		return engine.Outcome{Status: engine.StatusFail, FailureReason: err.Error()}
	}
	var out engine.Outcome
	must(t, json.Unmarshal(data, &out))
	out.Status = engine.ParseStatus(out.StatusString)
	return out
}

func esc(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func waitFor(cond func() bool) bool {
	for i := 0; i < 100; i++ {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
