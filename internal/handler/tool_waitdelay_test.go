package handler

import (
	"testing"
	"time"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/runstore"
)

// TestToolReturnsWhenOrphanHoldsStdout reproduces the wedge that hung a run for
// hours: a tool_command spawns a background grandchild that outlives the shell
// and keeps the stdout pipe open. Without exe.WaitDelay, cmd.Wait blocks
// forever copying from that pipe even though the shell itself has exited.
// Execute must return promptly instead (WaitDelay abandons the pipe copy after
// the process exits), reporting a failure rather than hanging.
func TestToolReturnsWhenOrphanHoldsStdout(t *testing.T) {
	stage := runstore.New(t.TempDir())
	if err := stage.MkdirAll(); err != nil {
		t.Fatal(err)
	}
	// The shell backgrounds `sleep 20` (which inherits the stdout pipe), prints
	// a line, and exits 0. The orphaned sleep holds the pipe open for 20s — long
	// enough that a hang is unambiguous — while WaitDelay is only a few seconds.
	src := `digraph d {
		work [type="tool", tool_command="sleep 20 & echo hi"]
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

	done := make(chan engine.Outcome, 1)
	go func() { done <- Tool{}.Execute(env) }()

	select {
	case <-done:
		// Returned promptly — WaitDelay released the orphan-held pipe.
	case <-time.After(15 * time.Second):
		t.Fatal("Execute hung on an orphan holding the stdout pipe — WaitDelay not applied")
	}
}

// TestToolTimeoutKillsProcessTree proves a tool_command that exceeds its
// timeout is killed as a whole process group: a background grandchild that
// would otherwise hold the pipe open past the kill is reaped too, so Execute
// returns promptly with the timeout failure instead of blocking on Wait.
func TestToolTimeoutKillsProcessTree(t *testing.T) {
	stage := runstore.New(t.TempDir())
	if err := stage.MkdirAll(); err != nil {
		t.Fatal(err)
	}
	// The shell stays alive past the timeout (its own `sleep 20`) and also
	// backgrounds a second long sleep holding the pipe. On timeout the whole
	// group must die.
	src := `digraph d {
		work [type="tool", tool_command="sleep 20 & sleep 20", timeout="500ms"]
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

	done := make(chan engine.Outcome, 1)
	start := time.Now()
	go func() { done <- Tool{}.Execute(env) }()

	select {
	case out := <-done:
		if out.Status != engine.StatusFail {
			t.Fatalf("status = %v, want fail (timeout)", out.Status)
		}
		if elapsed := time.Since(start); elapsed > 12*time.Second {
			t.Fatalf("Execute took %v after a 500ms timeout — process tree not killed", elapsed)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Execute hung after timeout — process tree not killed and WaitDelay not applied")
	}
}
