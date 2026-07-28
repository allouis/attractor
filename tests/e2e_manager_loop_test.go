package attractor_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
)

func TestManagerLoop_SupervisesChildToSuccess(t *testing.T) {
	childPath, err := filepath.Abs("../testdata/pipelines/child.dot")
	must(t, err)
	src := fmt.Sprintf(`digraph supervised {
		stack.child_dotfile = "%s"
		start [shape=Mdiamond]
		boss [
			shape=house,
			manager.actions="observe,wait",
			manager.poll_interval="50ms",
			manager.max_cycles=200
		]
		done [shape=Msquare]
		start -> boss -> done
	}`, childPath)

	be := fake.New()
	be.SetText("do", "child reply")
	out, _, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("manager_loop status=%s reason=%q", out.Status, out.FailureReason)
	}
}

func TestManagerLoop_PropagatesChildFailure(t *testing.T) {
	childPath, err := filepath.Abs("../testdata/pipelines/child.dot")
	must(t, err)
	src := fmt.Sprintf(`digraph supervised {
		stack.child_dotfile = "%s"
		start [shape=Mdiamond]
		boss [
			shape=house,
			manager.poll_interval="20ms",
			manager.max_cycles=100
		]
		done [shape=Msquare]
		start -> boss -> done
	}`, childPath)

	be := fake.New()
	be.SetSequence("do", fake.Step{Outcome: &engine.Outcome{
		Status: engine.StatusFail, FailureReason: "child broke",
	}})
	out, _, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusFail {
		t.Fatalf("expected FAIL when child fails, got %s", out.Status)
	}
	if !strings.Contains(out.FailureReason, "child") {
		t.Fatalf("failure reason should reference child: %q", out.FailureReason)
	}
}

func TestManagerLoop_MissingChildDotfile(t *testing.T) {
	src := `digraph supervised {
		start [shape=Mdiamond]
		boss [shape=house, manager.poll_interval="10ms"]
		done [shape=Msquare]
		start -> boss -> done
	}`
	out, _, _ := runFixture(t, src, fake.New(), nil)
	if out.Status != engine.StatusFail {
		t.Fatalf("expected FAIL, got %s", out.Status)
	}
	if !strings.Contains(out.FailureReason, "stack.child_dotfile") {
		t.Fatalf("error message should mention child_dotfile: %q", out.FailureReason)
	}
}

func TestManagerLoop_NodeChildDotfile(t *testing.T) {
	// The child .dot is declared as a NODE attribute; the graph carries
	// no stack.child_dotfile. The node attr must be read.
	childPath, err := filepath.Abs("../testdata/pipelines/child.dot")
	must(t, err)
	src := fmt.Sprintf(`digraph supervised {
		start [shape=Mdiamond]
		boss [
			shape=house,
			stack.child_dotfile="%s",
			manager.actions="observe,wait",
			manager.poll_interval="10ms",
			manager.max_cycles=200
		]
		done [shape=Msquare]
		start -> boss -> done
	}`, childPath)

	be := fake.New()
	be.SetText("do", "child reply")
	out, _, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("manager_loop status=%s reason=%q", out.Status, out.FailureReason)
	}
}

func TestManagerLoop_TwoNodesDistinctChildren(t *testing.T) {
	// Two manager_loop nodes in one graph, each with its own node-level
	// stack.child_dotfile, run their own child (R1 testing convention).
	childPath, err := filepath.Abs("../testdata/pipelines/child.dot")
	must(t, err)
	altPath, err := filepath.Abs("../testdata/pipelines/child_alt.dot")
	must(t, err)
	src := fmt.Sprintf(`digraph supervised {
		start [shape=Mdiamond]
		boss1 [
			shape=house,
			stack.child_dotfile="%s",
			manager.poll_interval="10ms",
			manager.max_cycles=200
		]
		boss2 [
			shape=house,
			stack.child_dotfile="%s",
			manager.poll_interval="10ms",
			manager.max_cycles=200
		]
		done [shape=Msquare]
		start -> boss1 -> boss2 -> done
	}`, childPath, altPath)

	be := fake.New()
	be.SetText("do", "child reply")
	be.SetText("alt", "alt reply")
	out, _, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("two-child manager_loop status=%s reason=%q", out.Status, out.FailureReason)
	}
	// Each boss ran its OWN child: child.dot's `do` node and
	// child_alt.dot's `alt` node were each invoked exactly once. If a
	// boss read the wrong child_dotfile, one of these counts would be 0.
	if got := be.CallCount("do"); got != 1 {
		t.Fatalf("child.dot `do` node ran %d times, want 1 (boss1's child)", got)
	}
	if got := be.CallCount("alt"); got != 1 {
		t.Fatalf("child_alt.dot `alt` node ran %d times, want 1 (boss2's child)", got)
	}
}

func TestManagerLoop_NodeChildWorkdir(t *testing.T) {
	// A node-level stack.child_workdir resolves a relative node-level
	// stack.child_dotfile; the graph carries neither.
	workdir, err := filepath.Abs("../testdata/pipelines")
	must(t, err)
	src := fmt.Sprintf(`digraph supervised {
		start [shape=Mdiamond]
		boss [
			shape=house,
			stack.child_dotfile="child.dot",
			stack.child_workdir="%s",
			manager.poll_interval="10ms",
			manager.max_cycles=200
		]
		done [shape=Msquare]
		start -> boss -> done
	}`, workdir)

	be := fake.New()
	be.SetText("do", "child reply")
	out, _, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("node-workdir manager_loop status=%s reason=%q", out.Status, out.FailureReason)
	}
}

func TestManagerLoop_ChildVarsFromContext(t *testing.T) {
	// The child declares vars="x" and expands $x into a prompt. The
	// manager_loop must source x from the parent run's context (seeded at
	// start) and prepare the child with it, so the child node sees the
	// substituted value (R3).
	childPath, err := filepath.Abs("../testdata/pipelines/child_vars.dot")
	must(t, err)
	src := fmt.Sprintf(`digraph supervised {
		start [shape=Mdiamond]
		boss [
			shape=house,
			stack.child_dotfile="%s",
			manager.poll_interval="10ms",
			manager.max_cycles=200
		]
		done [shape=Msquare]
		start -> boss -> done
	}`, childPath)

	be := fake.New()
	out, _, _ := runFixtureSeeded(t, src, be, nil, map[string]string{"x": "from-ctx"})
	if out.Status != engine.StatusSuccess {
		t.Fatalf("manager_loop status=%s reason=%q", out.Status, out.FailureReason)
	}
	if got := childPrompt(t, be, "childwork"); !strings.Contains(got, "value=from-ctx") {
		t.Fatalf("child prompt = %q, want it to contain %q (x sourced from context)", got, "value=from-ctx")
	}
}

func TestManagerLoop_ChildVarOverridesContext(t *testing.T) {
	// A stack.child.var.x node override beats the context value (R3).
	childPath, err := filepath.Abs("../testdata/pipelines/child_vars.dot")
	must(t, err)
	src := fmt.Sprintf(`digraph supervised {
		start [shape=Mdiamond]
		boss [
			shape=house,
			stack.child_dotfile="%s",
			stack.child.var.x="override",
			manager.poll_interval="10ms",
			manager.max_cycles=200
		]
		done [shape=Msquare]
		start -> boss -> done
	}`, childPath)

	be := fake.New()
	out, _, _ := runFixtureSeeded(t, src, be, nil, map[string]string{"x": "from-ctx"})
	if out.Status != engine.StatusSuccess {
		t.Fatalf("manager_loop status=%s reason=%q", out.Status, out.FailureReason)
	}
	if got := childPrompt(t, be, "childwork"); !strings.Contains(got, "value=override") {
		t.Fatalf("child prompt = %q, want it to contain %q (override beats context)", got, "value=override")
	}
}

func TestManagerLoop_ChildReadsUndeclaredContextKey(t *testing.T) {
	// The child references $context.pr_number but does NOT declare it in
	// vars=. The manager_loop seeds the child's initial context from the
	// full parent context (C6), so an undeclared key still resolves at the
	// child's runtime interpolation.
	childPath, err := filepath.Abs("../testdata/pipelines/child_ctx.dot")
	must(t, err)
	src := fmt.Sprintf(`digraph supervised {
		start [shape=Mdiamond]
		boss [
			shape=house,
			stack.child_dotfile="%s",
			manager.poll_interval="10ms",
			manager.max_cycles=200
		]
		done [shape=Msquare]
		start -> boss -> done
	}`, childPath)

	be := fake.New()
	out, _, _ := runFixtureSeeded(t, src, be, nil, map[string]string{"pr_number": "42"})
	if out.Status != engine.StatusSuccess {
		t.Fatalf("manager_loop status=%s reason=%q", out.Status, out.FailureReason)
	}
	if got := childPrompt(t, be, "childwork"); !strings.Contains(got, "id=42") {
		t.Fatalf("child prompt = %q, want it to contain %q (undeclared key from seeded context)", got, "id=42")
	}
}

// childPrompt returns the prompt the fake backend saw for the child node,
// failing if the node was never invoked.
func childPrompt(t *testing.T, be *fake.Backend, nodeID string) string {
	t.Helper()
	for _, c := range be.Calls() {
		if c.NodeID == nodeID {
			return c.Prompt
		}
	}
	t.Fatalf("child node %q never invoked", nodeID)
	return ""
}

func TestManagerLoop_PollerExitsCleanly(t *testing.T) {
	// Smoke that a fast-completing child doesn't leave the poller spinning.
	childPath, err := filepath.Abs("../testdata/pipelines/child.dot")
	must(t, err)
	src := fmt.Sprintf(`digraph supervised {
		stack.child_dotfile = "%s"
		start [shape=Mdiamond]
		boss [
			shape=house,
			manager.poll_interval="5ms",
			manager.max_cycles=2000
		]
		done [shape=Msquare]
		start -> boss -> done
	}`, childPath)

	be := fake.New()
	be.SetText("do", "ok")

	deadline := time.Now().Add(3 * time.Second)
	doneCh := make(chan engine.Outcome, 1)
	go func() {
		out, _, _ := runFixture(t, src, be, nil)
		doneCh <- out
	}()
	select {
	case out := <-doneCh:
		if out.Status != engine.StatusSuccess {
			t.Fatalf("status=%s", out.Status)
		}
	case <-time.After(time.Until(deadline)):
		t.Fatal("manager loop did not exit")
	}
}
