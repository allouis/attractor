package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
)

// TestCwd_ToolRunsInGraphCwd verifies that a graph-level `cwd` attribute
// sets the working directory of subsequent tool subprocesses.
func TestCwd_ToolRunsInGraphCwd(t *testing.T) {
	workspace := t.TempDir()
	must(t, os.WriteFile(filepath.Join(workspace, "marker.txt"), []byte("hi"), 0o644))

	src := `digraph t {
		cwd = "` + workspace + `"
		start [shape=Mdiamond]
		probe [shape=parallelogram, tool_command="pwd && cat marker.txt"]
		done [shape=Msquare]
		start -> probe -> done
	}`
	_, _, logs := runFixture(t, src, fake.New(), nil)
	stdout, err := os.ReadFile(filepath.Join(logs, "probe", "stdout.txt"))
	must(t, err)
	body := string(stdout)
	if !strings.Contains(body, workspace) {
		t.Fatalf("tool didn't run in graph cwd; pwd was: %q", body)
	}
	if !strings.Contains(body, "hi") {
		t.Fatalf("tool couldn't read marker.txt from cwd; got: %q", body)
	}
}

// TestCwd_NodeOverridesGraph verifies node-level `cwd` wins over the
// graph-level default.
func TestCwd_NodeOverridesGraph(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	must(t, os.WriteFile(filepath.Join(a, "from_a.txt"), []byte("a"), 0o644))
	must(t, os.WriteFile(filepath.Join(b, "from_b.txt"), []byte("b"), 0o644))

	src := `digraph t {
		cwd = "` + a + `"
		start [shape=Mdiamond]
		probe [shape=parallelogram, cwd="` + b + `", tool_command="cat from_b.txt"]
		done [shape=Msquare]
		start -> probe -> done
	}`
	_, _, logs := runFixture(t, src, fake.New(), nil)
	stdout, err := os.ReadFile(filepath.Join(logs, "probe", "stdout.txt"))
	must(t, err)
	if !strings.Contains(string(stdout), "b") {
		t.Fatalf("node-level cwd did not override graph cwd; stdout=%q", stdout)
	}
}
