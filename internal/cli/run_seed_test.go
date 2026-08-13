package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/interviewer"
	"github.com/allouis/attractor/internal/setup"
)

// A tool node that fails fast unless `$context.k` resolves, so a run's
// success proves the CLI seeded `-var k=v` into the initial context (C3).
const toolCtxGraph = `digraph d {
	start [shape=Mdiamond]
	work [type="tool", tool_command="echo $context.k"]
	done [shape=Msquare]
	start -> work
	work -> done
}`

func prepareToolGraph(t *testing.T) *engine.PreparedGraph {
	t.Helper()
	prepared, err := setup.Prepare(setup.Options{Source: toolCtxGraph})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	return prepared
}

func TestRunEngineSeedsVarIntoContext(t *testing.T) {
	err := runEngine(prepareToolGraph(t), fake.New(), interviewer.AutoApprove{}, t.TempDir(), false, map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("run with -var k=v: %v", err)
	}
}

func TestRunEngineFailsWithoutSeededVar(t *testing.T) {
	err := runEngine(prepareToolGraph(t), fake.New(), interviewer.AutoApprove{}, t.TempDir(), false, nil)
	if err == nil {
		t.Fatal("run without k: want error from unresolved $context.k, got nil")
	}
}

// The run writes graph.dot (the executed, transform-applied topology,
// re-emitted as minimal DOT) into its logs root so the run server's
// /graph endpoint — and the archive — can render it.
func TestRunWritesGraphDot(t *testing.T) {
	logs := t.TempDir()
	err := runEngine(prepareToolGraph(t), fake.New(), interviewer.AutoApprove{}, logs, false, map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(logs, "graph.dot"))
	if err != nil {
		t.Fatalf("graph.dot not written: %v", err)
	}
	if !strings.Contains(string(data), "digraph") {
		t.Fatalf("graph.dot not DOT: %.80s", data)
	}
}
