package attractor_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	graphpkg "github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/interviewer"
)

// runFixture builds a graph from src, registers handlers with the given
// backend + interviewer, and runs the engine to completion. Returns the
// terminal outcome and the populated logs directory for inspection.
func runFixture(t *testing.T, src string, be backend.CodergenBackend, iv interviewer.Interviewer) (engine.Outcome, []engine.Event, string) {
	t.Helper()
	logsRoot := t.TempDir()
	out, events := runFixtureIn(t, src, be, iv, logsRoot)
	return out, events, logsRoot
}

// runFixtureSeeded is runFixture with a seeded initial context (R2), so a
// handler can read values the run started with — e.g. a manager_loop
// sourcing its child's vars from context (R3).
func runFixtureSeeded(t *testing.T, src string, be backend.CodergenBackend, iv interviewer.Interviewer, seed map[string]string) (engine.Outcome, []engine.Event, string) {
	t.Helper()
	logsRoot := t.TempDir()
	out, events := runFixtureInSeeded(t, src, be, iv, logsRoot, seed)
	return out, events, logsRoot
}

func runFixtureIn(t *testing.T, src string, be backend.CodergenBackend, iv interviewer.Interviewer, logsRoot string) (engine.Outcome, []engine.Event) {
	t.Helper()
	return runFixtureInSeeded(t, src, be, iv, logsRoot, nil)
}

func runFixtureInSeeded(t *testing.T, src string, be backend.CodergenBackend, iv interviewer.Interviewer, logsRoot string, seed map[string]string) (engine.Outcome, []engine.Event) {
	t.Helper()
	file, err := dot.Parse(src)
	must(t, err)
	g, err := graphpkg.Build(file)
	must(t, err)
	prepared, err := engine.Prepare(g)
	must(t, err)
	registry := engine.NewRegistry()
	registry.Register("start", handler.Start{})
	registry.Register("exit", handler.Exit{})
	registry.Register("conditional", handler.Conditional{})
	registry.Register("codergen", handler.Codergen{Backend: be})
	registry.Register("codergen.claude", handler.Codergen{Backend: be})
	registry.Register("codergen.openai", handler.Codergen{Backend: be})
	registry.Register("wait.human", handler.WaitHuman{Interviewer: iv})
	registry.Register("tool", handler.Tool{})
	registry.Register("parallel", handler.Parallel{})
	registry.Register("parallel.fan_in", handler.FanIn{})
	registry.SetDefault(handler.Codergen{Backend: be})
	eng := engine.New(engine.Config{Registry: registry, LogsRoot: logsRoot, RunID: "test", InitialContext: seed})
	events := make([]engine.Event, 0)
	done := make(chan struct{})
	go func() {
		for ev := range eng.Events() {
			events = append(events, ev)
		}
		close(done)
	}()
	outcome, err := eng.Run(prepared)
	if err != nil && outcome.Status != engine.StatusFail {
		t.Fatalf("engine error: %v", err)
	}
	<-done
	return outcome, events
}

// readStatus reads a node's LATEST canonical status under the span-dir
// layout (A4): highest visit, then highest attempt.
func readStatus(t *testing.T, logsRoot, nodeID string) engine.Outcome {
	t.Helper()
	dir := latestSpanDir(t, logsRoot, nodeID)
	data, err := os.ReadFile(filepath.Join(logsRoot, dir, "status.json"))
	if err != nil {
		t.Fatalf("read status %s: %v", nodeID, err)
	}
	var out engine.Outcome
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal status %s: %v", nodeID, err)
	}
	out.Status = engine.ParseStatus(out.StatusString)
	return out
}

// latestSpanDir finds a node's newest {node}@v{V}.a{A} directory.
func latestSpanDir(t *testing.T, logsRoot, nodeID string) string {
	t.Helper()
	entries, err := os.ReadDir(logsRoot)
	if err != nil {
		t.Fatalf("read logs root: %v", err)
	}
	best, bestV, bestA := "", -1, -1
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		var v, a int
		if n, _ := fmt.Sscanf(ent.Name(), nodeID+"@v%d.a%d", &v, &a); n == 2 {
			if v > bestV || (v == bestV && a > bestA) {
				best, bestV, bestA = ent.Name(), v, a
			}
		}
	}
	if best == "" {
		t.Fatalf("no span dir for node %q under %s", nodeID, logsRoot)
	}
	return best
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// childPrompt returns the prompt the fake backend saw for a node, failing
// the test if the node was never invoked.
func childPrompt(t *testing.T, be *fake.Backend, nodeID string) string {
	t.Helper()
	for _, c := range be.Calls() {
		if c.NodeID == nodeID {
			return c.Prompt
		}
	}
	t.Fatalf("node %q never invoked", nodeID)
	return ""
}

// spanFileExists reports whether the node has any span dir containing
// the named file — for negative assertions ("this node never ran").
func spanFileExists(t *testing.T, logsRoot, nodeID, name string) bool {
	t.Helper()
	entries, err := os.ReadDir(logsRoot)
	if err != nil {
		return false
	}
	for _, ent := range entries {
		var v, a int
		if n, _ := fmt.Sscanf(ent.Name(), nodeID+"@v%d.a%d", &v, &a); n == 2 {
			if _, err := os.Stat(filepath.Join(logsRoot, ent.Name(), name)); err == nil {
				return true
			}
		}
	}
	return false
}

// spanPath resolves a file inside a node's latest span dir.
func spanPath(t *testing.T, logsRoot, nodeID, name string) string {
	t.Helper()
	return filepath.Join(logsRoot, latestSpanDir(t, logsRoot, nodeID), name)
}
