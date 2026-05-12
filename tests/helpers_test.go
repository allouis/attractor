package attractor_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/fabro/attractor/internal/backend"
	"github.com/fabro/attractor/internal/dot"
	"github.com/fabro/attractor/internal/engine"
	graphpkg "github.com/fabro/attractor/internal/graph"
	"github.com/fabro/attractor/internal/handler"
	"github.com/fabro/attractor/internal/interviewer"
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

func runFixtureIn(t *testing.T, src string, be backend.CodergenBackend, iv interviewer.Interviewer, logsRoot string) (engine.Outcome, []engine.Event) {
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
	registry.Register("stack.manager_loop", handler.ManagerLoop{})
	registry.SetDefault(handler.Codergen{Backend: be})
	eng := engine.New(engine.Config{Registry: registry, LogsRoot: logsRoot, RunID: "test"})
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

func readStatus(t *testing.T, logsRoot, nodeID string) engine.Outcome {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(logsRoot, nodeID, "status.json"))
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

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}
