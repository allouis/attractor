package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/backend/claudecode"
	"github.com/allouis/attractor/internal/graph"
)

// The claudecode backend supports in-session contract corrections (D2).
var _ backend.Continuer = (*claudecode.Backend)(nil)

// fakeClaudeRecording returns a fake claude binary that records its argv
// per invocation and reports a fixed session id in its stream output.
func fakeClaudeRecording(t *testing.T, argsFile string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	script := "#!" + bashPath(t) + "\n" +
		`echo "$@" >> ` + argsFile + "\n" +
		`echo '{"type":"system","subtype":"init","session_id":"sess-cc-1"}'` + "\n" +
		`echo '{"type":"result","subtype":"success","result":"ok","is_error":false}'` + "\n"
	must(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// TestClaudeCode_ContinueResumesSession: Continue re-invokes claude with
// --resume <the session the node's Run reported>, so the correction
// turn continues the same conversation.
func TestClaudeCode_ContinueResumesSession(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.log")
	be := &claudecode.Backend{ClaudeBin: fakeClaudeRecording(t, argsFile)}
	env, _ := acpEnv(t, &graph.Node{ID: "synth", Attrs: map[string]string{}})

	_, err := be.Run(env, "first turn")
	must(t, err)
	_, err = be.Continue(env, "write your status.json now")
	must(t, err)

	data, err := os.ReadFile(argsFile)
	must(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("claude invoked %d times, want 2; args: %q", len(lines), data)
	}
	if strings.Contains(lines[0], "--resume") {
		t.Fatalf("first turn must not resume: %q", lines[0])
	}
	if !strings.Contains(lines[1], "--resume sess-cc-1") {
		t.Fatalf("continue must pass --resume sess-cc-1: %q", lines[1])
	}
}

// TestClaudeCode_ContinueWithoutPriorRunErrors: no recorded session for
// the node → error, never a fresh contextless session.
func TestClaudeCode_ContinueWithoutPriorRunErrors(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.log")
	be := &claudecode.Backend{ClaudeBin: fakeClaudeRecording(t, argsFile)}
	env, _ := acpEnv(t, &graph.Node{ID: "synth", Attrs: map[string]string{}})
	if _, err := be.Continue(env, "correction"); err == nil {
		t.Fatal("Continue without a prior Run must error")
	}
}
