package handler

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/runstore"
)

// TestToolStreamsStdoutWhileRunning proves the tool handler streams stdout to
// the stage file WHILE the command runs (ui-run-view-v3 P5b), not only after it
// exits: a poller reading stdout.txt during Execute sees the first line before
// the command (which sleeps between lines) finishes.
func TestToolStreamsStdoutWhileRunning(t *testing.T) {
	stage := runstore.New(t.TempDir())
	if err := stage.MkdirAll(); err != nil {
		t.Fatal(err)
	}
	src := `digraph d {
		work [type="tool", tool_command="echo first; sleep 1; echo second"]
	}`
	file, err := dot.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g, err := graph.Build(file)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	env := engine.HandlerEnv{Node: g.Nodes["work"], Graph: g, Context: engine.NewContext(), Stage: stage}

	stdoutPath := filepath.Join(stage.Root(), "stdout.txt")
	var sawFirstEarly atomic.Bool
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, _ := os.ReadFile(stdoutPath)
			s := string(data)
			// "first" on disk while "second" is not yet written proves the file
			// grew mid-command rather than being flushed once at exit.
			if strings.Contains(s, "first") && !strings.Contains(s, "second") {
				sawFirstEarly.Store(true)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	out := Tool{}.Execute(env)
	close(stop)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status = %v (%s)", out.Status, out.FailureReason)
	}
	if !sawFirstEarly.Load() {
		t.Fatal("first line was not on disk before the command finished — stdout is buffered, not streamed")
	}
	// Final content is still the full output (in-memory buffer preserved).
	data, err := os.ReadFile(stdoutPath)
	if err != nil {
		t.Fatalf("read stdout.txt: %v", err)
	}
	if !strings.Contains(string(data), "first") || !strings.Contains(string(data), "second") {
		t.Fatalf("stdout.txt missing final content: %q", data)
	}
	if got := out.ContextUpdates["tool.output"]; !strings.Contains(got, "second") {
		t.Fatalf("tool.output context update missing content: %q", got)
	}
}
