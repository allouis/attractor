package attractor_test

import (
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	graphpkg "github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/handler"
)

// TestEngine_SeededInitialContext proves a run started with seeded initial
// values exposes them to the first node: the classify conditional branches
// on the seeded `item.type` (router-spec deviation B / R2), which lives in
// no graph attr and no outcome — only the seed can supply it.
func TestEngine_SeededInitialContext(t *testing.T) {
	src := `digraph seed {
		start [shape=Mdiamond]
		classify [type="conditional"]
		pr_path [prompt="review"]
		issue_path [prompt="implement"]
		done [shape=Msquare]
		start -> classify
		classify -> pr_path    [condition="item.type=pr"]
		classify -> issue_path [condition="item.type=issue"]
		pr_path -> done
		issue_path -> done
	}`

	logs := runSeeded(t, src, map[string]string{"item.type": "pr"})

	if !fileExists(t, spanPath(t, logs, "pr_path", "status.json")) {
		t.Fatal("expected pr_path taken (seeded item.type=pr)")
	}
	if spanFileExists(t, logs, "issue_path", "status.json") {
		t.Fatal("did not expect issue_path taken")
	}
}

// runSeeded builds src, runs it with the given seeded initial context, and
// returns the logs dir for inspection.
func runSeeded(t *testing.T, src string, seed map[string]string) string {
	t.Helper()
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
	registry.Register("conditional", handler.Conditional{})
	registry.Register("tool", handler.Tool{})
	registry.SetDefault(handler.Codergen{Backend: fake.New()})
	eng := engine.New(engine.Config{
		Registry:       registry,
		LogsRoot:       logs,
		RunID:          "test",
		InitialContext: seed,
	})
	done := make(chan struct{})
	go func() {
		for range eng.Events() {
		}
		close(done)
	}()
	if _, err := eng.Run(prepared); err != nil {
		t.Fatalf("run: %v", err)
	}
	<-done
	return logs
}
