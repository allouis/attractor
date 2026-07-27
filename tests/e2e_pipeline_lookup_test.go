package attractor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fabro/attractor/internal/cli"
)

// TestCLI_ResolvesPipelineFromDotAttractorPipelines verifies that a bare
// pipeline name resolves from ~/.attractor/pipelines/<name>/pipeline.dot
// (the canonical per-user pipeline library location).
func TestCLI_ResolvesPipelineFromDotAttractorPipelines(t *testing.T) {
	home := t.TempDir()
	pdir := filepath.Join(home, ".attractor", "pipelines", "lookup-demo")
	must(t, os.MkdirAll(pdir, 0o755))
	dot := `digraph g {
		start [shape=Mdiamond]
		work [prompt="do the thing"]
		done [shape=Msquare]
		start -> work -> done
	}`
	must(t, os.WriteFile(filepath.Join(pdir, "pipeline.dot"), []byte(dot), 0o644))

	oldHome := os.Getenv("HOME")
	must(t, os.Setenv("HOME", home))
	t.Cleanup(func() { _ = os.Setenv("HOME", oldHome) })

	logsRoot := t.TempDir()
	err := cli.Run([]string{"--backend", "simulation", "--logs", logsRoot, "lookup-demo"})
	must(t, err)

	if _, err := os.Stat(filepath.Join(logsRoot, "work", "response.md")); err != nil {
		t.Fatalf("pipeline from ~/.attractor/pipelines did not resolve/run: %v", err)
	}
}

// TestCLI_ResolvesSingleFilePipelineFromDotAttractorPipelines verifies the
// flat ~/.attractor/pipelines/<name>.dot form resolves too.
func TestCLI_ResolvesSingleFilePipelineFromDotAttractorPipelines(t *testing.T) {
	home := t.TempDir()
	pdir := filepath.Join(home, ".attractor", "pipelines")
	must(t, os.MkdirAll(pdir, 0o755))
	dot := `digraph g {
		start [shape=Mdiamond]
		work [prompt="do the thing"]
		done [shape=Msquare]
		start -> work -> done
	}`
	must(t, os.WriteFile(filepath.Join(pdir, "flat-demo.dot"), []byte(dot), 0o644))

	oldHome := os.Getenv("HOME")
	must(t, os.Setenv("HOME", home))
	t.Cleanup(func() { _ = os.Setenv("HOME", oldHome) })

	logsRoot := t.TempDir()
	err := cli.Run([]string{"--backend", "simulation", "--logs", logsRoot, "flat-demo"})
	must(t, err)

	if _, err := os.Stat(filepath.Join(logsRoot, "work", "response.md")); err != nil {
		t.Fatalf("flat pipeline from ~/.attractor/pipelines did not resolve/run: %v", err)
	}
}
