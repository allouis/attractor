package attractor_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend/claudecode"
	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	graphpkg "github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/handler"
)

// TestSessionReuse_FullFidelityPassesResume verifies the §5.4 session
// reuse contract: when the engine resolves full fidelity for a node
// whose thread_id we've seen before, the claudecode backend invokes
// claude with --resume <session-id>.
func TestSessionReuse_FullFidelityPassesResume(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "claude.calls")
	claudeBin := filepath.Join(dir, "claude")
	// Fake claude records its argv to a file then emits a stream-json
	// pair with a session_id so the backend captures it on call N=1 and
	// resumes with it on call N=2.
	script := "#!" + bashPath(t) + "\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n" +
		`echo '{"type":"system","subtype":"init","session_id":"sess-abc-123"}'` + "\n" +
		`echo '{"type":"result","subtype":"success","session_id":"sess-abc-123","result":"ok","is_error":false}'` + "\n"
	must(t, os.WriteFile(claudeBin, []byte(script), 0o755))

	be := &claudecode.Backend{ClaudeBin: claudeBin}

	src := `digraph t {
		goal = "session reuse smoke"
		node [fidelity="full", thread_id="conv"]
		start [shape=Mdiamond]
		a [prompt="first turn"]
		b [prompt="second turn"]
		done [shape=Msquare]
		start -> a -> b -> done
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
	eng := engine.New(engine.Config{Registry: registry, LogsRoot: t.TempDir(), RunID: "test"})
	go func() {
		for range eng.Events() {
		}
	}()
	_, err = eng.Run(prepared)
	must(t, err)

	data, err := os.ReadFile(logPath)
	must(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two claude invocations, got %d: %q", len(lines), lines)
	}
	if strings.Contains(lines[0], "--resume") {
		t.Fatalf("first call should NOT carry --resume: %q", lines[0])
	}
	if !strings.Contains(lines[1], "--resume sess-abc-123") {
		t.Fatalf("second call should carry --resume sess-abc-123: %q", lines[1])
	}
}

// TestSessionReuse_FreshSessionForCompactMode confirms that the backend
// does NOT reuse the session when the resolved fidelity is anything
// other than full — even if a session_id has been captured.
func TestSessionReuse_FreshSessionForCompactMode(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "claude.calls")
	claudeBin := filepath.Join(dir, "claude")
	script := "#!" + bashPath(t) + "\n" +
		"printf '%s\\n' \"$*\" >> " + logPath + "\n" +
		`echo '{"type":"system","subtype":"init","session_id":"sess-xyz"}'` + "\n" +
		`echo '{"type":"result","subtype":"success","session_id":"sess-xyz","result":"ok","is_error":false}'` + "\n"
	must(t, os.WriteFile(claudeBin, []byte(script), 0o755))

	be := &claudecode.Backend{ClaudeBin: claudeBin}

	src := `digraph t {
		default_fidelity = "compact"
		start [shape=Mdiamond]
		a [prompt="first"]
		b [prompt="second"]
		done [shape=Msquare]
		start -> a -> b -> done
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
	eng := engine.New(engine.Config{Registry: registry, LogsRoot: t.TempDir(), RunID: "test"})
	go func() {
		for range eng.Events() {
		}
	}()
	_, err = eng.Run(prepared)
	must(t, err)

	data, err := os.ReadFile(logPath)
	must(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i, line := range lines {
		if strings.Contains(line, "--resume") {
			t.Fatalf("call %d carried --resume in compact mode: %q", i+1, line)
		}
	}
	fmt.Printf("ok %d compact-mode calls, none with --resume\n", len(lines))
}
