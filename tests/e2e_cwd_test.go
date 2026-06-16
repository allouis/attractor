package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/backend/fake"
	"github.com/fabro/attractor/internal/cli"
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

// TestCwd_VariableExpansionAppliesToCwd verifies that --var values flow
// into the cwd attribute. This is what the bug-fix pipeline relies on
// to point at $repo_dir.
func TestCwd_VariableExpansionAppliesToCwd(t *testing.T) {
	workspace := t.TempDir()
	must(t, os.WriteFile(filepath.Join(workspace, "from_var.txt"), []byte("ok"), 0o644))

	dotPath := filepath.Join(t.TempDir(), "pipeline.dot")
	must(t, os.WriteFile(dotPath, []byte(`digraph t {
		vars = "repo_dir"
		cwd = "$repo_dir"
		start [shape=Mdiamond]
		probe [shape=parallelogram, tool_command="cat from_var.txt"]
		done [shape=Msquare]
		start -> probe -> done
	}`), 0o644))

	logsRoot := t.TempDir()
	err := cli.Run([]string{
		"--backend", "simulation",
		"--logs", logsRoot,
		"--var", "repo_dir=" + workspace,
		dotPath,
	})
	must(t, err)

	stdout, err := os.ReadFile(filepath.Join(logsRoot, "probe", "stdout.txt"))
	must(t, err)
	if !strings.Contains(string(stdout), "ok") {
		t.Fatalf("$repo_dir didn't expand into cwd; stdout=%q", stdout)
	}
}
