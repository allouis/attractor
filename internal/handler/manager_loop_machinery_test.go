package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/runstore"
)

// runManagerLoopOverChild drives a manager_loop node over a child pipeline
// whose only working node is `synth` (a require_status verdict node), returning
// the manager_loop outcome and the parent context (so a test can read the
// surfaced stack.child.* telemetry the fix node consumes). The backend is
// invoked for the child's synth node.
func runManagerLoopOverChild(t *testing.T, be backend.CodergenBackend) (engine.Outcome, *engine.Context) {
	t.Helper()
	dir := t.TempDir()
	childPath := filepath.Join(dir, "child.dot")
	if err := os.WriteFile(childPath, []byte(`digraph child {
		start [shape=Mdiamond]
		synth [require_status="true"]
		done [shape=Msquare]
		start -> synth -> done
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := engine.NewRegistry()
	registry.Register("start", Start{})
	registry.Register("exit", Exit{})
	registry.Register("codergen", Codergen{Backend: be})
	registry.SetDefault(Codergen{Backend: be})

	node := &graph.Node{ID: "boss", Attrs: map[string]string{
		"type":                  "stack.manager_loop",
		"stack.child_dotfile":   childPath,
		"manager.poll_interval": "5ms",
		"manager.max_cycles":    "200",
	}}
	ctx := engine.NewContext()
	env := engine.HandlerEnv{
		Node:     node,
		Graph:    &graph.Graph{Attrs: map[string]string{}},
		Context:  ctx,
		Stage:    runstore.New(filepath.Join(dir, "boss")),
		Registry: registry,
		RunID:    "test",
	}
	out := ManagerLoop{}.Execute(env)
	// Mirror what the engine does at node completion (run.go): the outcome's
	// ContextUpdates are applied over the run context, so the downstream fix
	// node reads the authoritative post-node value of stack.child.failure_reason.
	ctx.Apply(out.ContextUpdates)
	return out, ctx
}

// When the child's synth is a require_status miss (verdict never written),
// manager_loop must surface its OWN node as a MACHINERY failure (a
// machinery-classed RETRY per LG1) — not a review FAIL verdict routed
// downstream. Feeding a harness error to a fix agent as if it were a
// finding is what wedged the a5ac1389 review loop.
func TestManagerLoop_RequireStatusMissIsMachineryFailure(t *testing.T) {
	shortGrace(t)
	be := backend.Func(func(_ engine.HandlerEnv, _ string) (backend.Result, error) {
		return backend.Result{ResponseText: "prose, but no status.json"}, nil
	})
	out, ctx := runManagerLoopOverChild(t, be)
	if out.Status != engine.StatusRetry {
		t.Fatalf("status = %v, want machinery retry (LG1)", out.Status)
	}
	if out.FailureClass != engine.FailureClassMachinery {
		t.Fatalf("failure class = %q, want machinery", out.FailureClass)
	}
	if !strings.Contains(out.FailureReason, "machinery failed (not a verdict)") {
		t.Fatalf("require_status miss should read as machinery failure, got: %q", out.FailureReason)
	}
	// The fix node reads $context.stack.child.failure_reason — it must carry
	// the machinery label, not the raw harness path presented as a finding.
	if got := ctx.Get("stack.child.failure_reason"); !strings.Contains(got, "machinery failed (not a verdict)") {
		t.Fatalf("surfaced stack.child.failure_reason should read as machinery failure, got: %q", got)
	}
}

// A child synth that WROTE a real FAIL verdict is a verdict, not machinery — it
// keeps the plain "child failed" path so it routes downstream to a fix round.
func TestManagerLoop_RealVerdictIsNotMachineryFailure(t *testing.T) {
	be := backend.Func(func(e engine.HandlerEnv, _ string) (backend.Result, error) {
		os.WriteFile(filepath.Join(e.Stage.Root(), "status.json"),
			[]byte(`{"outcome":"fail","failure_reason":"blocking defect found"}`), 0o644)
		return backend.Result{ResponseText: "wrote verdict"}, nil
	})
	out, ctx := runManagerLoopOverChild(t, be)
	if out.Status != engine.StatusFail {
		t.Fatalf("status = %v, want fail", out.Status)
	}
	if strings.Contains(out.FailureReason, "machinery") {
		t.Fatalf("a real FAIL verdict must not be flagged as machinery failure: %q", out.FailureReason)
	}
	if !strings.Contains(out.FailureReason, "blocking defect found") {
		t.Fatalf("verdict reason should propagate downstream: %q", out.FailureReason)
	}
	if got := ctx.Get("stack.child.failure_reason"); got != "blocking defect found" {
		t.Fatalf("verdict should reach the fix node verbatim, got: %q", got)
	}
}
