package attractor_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	graphpkg "github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/handler"
)

// TestEngine_GoalResolvedAtRunStart proves the graph `goal` is expanded
// once at run-start from the seeded context (spec decision 7): the run
// summary (run.json) and every node's `$goal` see the resolved text,
// never the raw `$context.*` placeholder.
func TestEngine_GoalResolvedAtRunStart(t *testing.T) {
	src := `digraph goalres {
		goal="PR #$context.pr_number"
		start [shape=Mdiamond]
		work [prompt="Working on $goal"]
		done [shape=Msquare]
		start -> work
		work -> done
	}`

	logs := runSeeded(t, src, map[string]string{"pr_number": "42"})

	// Run summary reflects the resolved goal.
	data, err := os.ReadFile(filepath.Join(logs, "run.json"))
	must(t, err)
	var m engine.Manifest
	must(t, json.Unmarshal(data, &m))
	if m.Goal != "PR #42" {
		t.Fatalf("manifest goal = %q, want %q", m.Goal, "PR #42")
	}

	// A node's `$goal` resolves through the frozen context, not the raw attr.
	prompt, err := os.ReadFile(filepath.Join(logs, "work", "prompt.md"))
	must(t, err)
	if !strings.Contains(string(prompt), "PR #42") {
		t.Fatalf("work prompt missing resolved goal: %q", prompt)
	}
	if strings.Contains(string(prompt), "$context.pr_number") {
		t.Fatalf("work prompt still has raw placeholder: %q", prompt)
	}
}

// TestEngine_GoalUnresolvedFailsAtStart proves a goal referencing a key
// absent from the seeded context fails the run fast at start (decision 4),
// naming the key — distinct from the `vars=` input-contract check.
func TestEngine_GoalUnresolvedFailsAtStart(t *testing.T) {
	src := `digraph goalfail {
		goal="PR #$context.missing"
		start [shape=Mdiamond]
		work [prompt="go"]
		done [shape=Msquare]
		start -> work
		work -> done
	}`

	file, err := dot.Parse(src)
	must(t, err)
	g, err := graphpkg.Build(file)
	must(t, err)
	prepared, err := engine.Prepare(g)
	must(t, err)
	logs := t.TempDir()
	registry := engine.NewRegistry()
	registry.Register("start", handler.Start{})
	registry.Register("exit", handler.Exit{})
	registry.SetDefault(handler.Codergen{Backend: fake.New()})
	eng := engine.New(engine.Config{Registry: registry, LogsRoot: logs, RunID: "test"})
	go func() {
		for range eng.Events() {
		}
	}()
	_, err = eng.Run(prepared)
	if err == nil {
		t.Fatal("expected run to fail on unresolved goal")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error should name the key: %v", err)
	}
	// Fail-fast: no node ran, so no work stage dir.
	if fileExists(t, filepath.Join(logs, "work", "status.json")) {
		t.Fatal("work node ran despite unresolved goal at start")
	}
}
