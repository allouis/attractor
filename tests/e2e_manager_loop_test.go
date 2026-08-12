package attractor_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	graphpkg "github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/runstore"
)

// TestManagerLoop_RevisitRerunsChildFresh pins the per-invocation freshness
// contract: a revisited manager_loop node (a review fix round re-entering the
// review child) must RE-RUN the child from scratch against the changed
// context, not resume the prior round's checkpoint and replay its cached
// output. Regression guard for run a5ac1389, where the review loop could never
// converge because the child kept replaying round 1's lens verdicts.
func TestManagerLoop_RevisitRerunsChildFresh(t *testing.T) {
	dir := t.TempDir()
	childPath := filepath.Join(dir, "child.dot")
	must(t, os.WriteFile(childPath, []byte(`digraph child {
		start [shape=Mdiamond]
		childwork [prompt="review diff=$context.diff"]
		done [shape=Msquare]
		start -> childwork -> done
	}`), 0o644))

	parentSrc := fmt.Sprintf(`digraph parent {
		start [shape=Mdiamond]
		boss [
			type="stack.manager_loop",
			stack.child_dotfile="%s",
			manager.poll_interval="5ms",
			manager.max_cycles=200
		]
		done [shape=Msquare]
		start -> boss -> done
	}`, childPath)

	file, err := dot.Parse(parentSrc)
	must(t, err)
	g, err := graphpkg.Build(file)
	must(t, err)
	prepared, err := engine.Prepare(g)
	must(t, err)
	bossNode := prepared.Graph.Nodes["boss"]
	if bossNode == nil {
		t.Fatal("boss node missing from prepared graph")
	}

	be := fake.New()
	registry := engine.NewRegistry()
	registry.Register("start", handler.Start{})
	registry.Register("exit", handler.Exit{})
	registry.Register("codergen", handler.Codergen{Backend: be})
	registry.SetDefault(handler.Codergen{Backend: be})

	// A single Context and Stage shared across both Execute calls — exactly
	// what the engine hands a revisited node (store.Sub(nodeID) is stable, and
	// the run context persists across revisits).
	ctx := engine.NewContext()
	stage := runstore.New(t.TempDir())

	env := engine.HandlerEnv{
		Node:     bossNode,
		Graph:    prepared.Graph,
		Context:  ctx,
		Stage:    stage,
		Registry: registry,
		RunID:    "test",
	}

	// Round 1: diff="v1".
	ctx.Set("diff", "v1")
	out := handler.ManagerLoop{}.Execute(env)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("round 1 status=%s reason=%q", out.Status, out.FailureReason)
	}
	if got := be.CallCount("childwork"); got != 1 {
		t.Fatalf("round 1: childwork ran %d times, want 1", got)
	}

	// Round 2: the diff changed. A revisit must re-run the child, not resume.
	ctx.Set("diff", "v2")
	out = handler.ManagerLoop{}.Execute(env)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("round 2 status=%s reason=%q", out.Status, out.FailureReason)
	}
	if got := be.CallCount("childwork"); got != 2 {
		t.Fatalf("round 2: childwork ran %d times total, want 2 — child resumed a stale checkpoint instead of re-running", got)
	}
	// The re-run must have read the CHANGED diff, not the cached round-1 value.
	calls := be.Calls()
	last := calls[len(calls)-1]
	if last.NodeID != "childwork" || !strings.Contains(last.Prompt, "diff=v2") {
		t.Fatalf("round 2 child prompt = %q, want it to contain %q (re-read changed diff)", last.Prompt, "diff=v2")
	}
}

// TestManagerLoop_ChildInheritsParentAcpCommand verifies a child pipeline
// with no acp_command inherits the parent's, so a reusable sub-pipeline
// (e.g. review-core) runs under a standalone `--backend acp` run without
// needing its own acp_command attr or a --acp-cmd flag.
func TestManagerLoop_ChildInheritsParentAcpCommand(t *testing.T) {
	dir := t.TempDir()
	childPath := filepath.Join(dir, "child.dot")
	must(t, os.WriteFile(childPath, []byte(`digraph child {
		start [shape=Mdiamond]
		childwork [prompt="x"]
		done [shape=Msquare]
		start -> childwork -> done
	}`), 0o644))

	parentSrc := fmt.Sprintf(`digraph parent {
		acp_command="claude-agent-acp"
		start [shape=Mdiamond]
		loop [type="stack.manager_loop", stack.child_dotfile="%s"]
		done [shape=Msquare]
		start -> loop -> done
	}`, childPath)

	var childAcp string
	be := backend.Func(func(env engine.HandlerEnv, prompt string) (backend.Result, error) {
		if env.Node.ID == "childwork" {
			childAcp = env.Graph.Attrs["acp_command"]
		}
		return backend.Result{ResponseText: "ok"}, nil
	})

	out, _, _ := runFixture(t, parentSrc, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q", out.Status, out.FailureReason)
	}
	if childAcp != "claude-agent-acp" {
		t.Fatalf("child did not inherit parent acp_command; got %q", childAcp)
	}
}

// TestManagerLoop_ResolvesChildRelativeToPipelineDir verifies that a
// relative stack.child_dotfile is resolved against the parent pipeline's
// directory (its BaseDir), not the process working directory — so
// `attractor run some/dir/parent.dot` works from anywhere and a relative
// "../sibling/child.dot" reference holds regardless of cwd.
func TestManagerLoop_ResolvesChildRelativeToPipelineDir(t *testing.T) {
	baseDir := t.TempDir()
	childName := "child_ml_reltest.dot"
	childSrc := `digraph child {
		start [shape=Mdiamond]
		work  [prompt="hi"]
		done  [shape=Msquare]
		start -> work -> done
	}`
	must(t, os.WriteFile(filepath.Join(baseDir, childName), []byte(childSrc), 0o644))

	// Sanity: the relative name must NOT resolve from the test's cwd, so a
	// pass can only come from BaseDir-relative resolution.
	if _, err := os.Stat(childName); err == nil {
		t.Skipf("%s unexpectedly exists in cwd", childName)
	}

	parentSrc := fmt.Sprintf(`digraph parent {
		start [shape=Mdiamond]
		loop  [type="stack.manager_loop", stack.child_dotfile="%s"]
		done  [shape=Msquare]
		start -> loop -> done
	}`, childName)

	out, _ := runFixtureBaseDir(t, parentSrc, baseDir, fake.New())
	if out.Status != engine.StatusSuccess {
		t.Fatalf("relative child not resolved via BaseDir: status=%s reason=%q", out.Status, out.FailureReason)
	}
}

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

func TestManagerLoop_ChildVarValueInterpolatesParentContext(t *testing.T) {
	// A stack.child.var.x value carrying a $context.<key> placeholder is
	// expanded against the PARENT live context at seed time, so the child
	// sees the resolved value — not the literal placeholder. This is what
	// lets review.dot seed diff_cmd="gh pr diff $context.pr_number ..."
	// (review-pipeline-spec RV3); the child's single-pass interpolation of
	// $context.diff_cmd can't resolve a nested placeholder on its own.
	childPath, err := filepath.Abs("../testdata/pipelines/child_vars.dot")
	must(t, err)
	src := fmt.Sprintf(`digraph supervised {
		start [shape=Mdiamond]
		boss [
			shape=house,
			stack.child_dotfile="%s",
			stack.child.var.x="pr=$context.pr_number",
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
	if got := childPrompt(t, be, "childwork"); !strings.Contains(got, "value=pr=42") {
		t.Fatalf("child prompt = %q, want it to contain %q (override interpolated against parent context)", got, "value=pr=42")
	}
}

func TestManagerLoop_ChildVarUnresolvedFails(t *testing.T) {
	// A stack.child.var override referencing a key absent from the parent
	// context fails loudly, naming the key — a mangled child command must
	// not run silently.
	childPath, err := filepath.Abs("../testdata/pipelines/child_vars.dot")
	must(t, err)
	src := fmt.Sprintf(`digraph supervised {
		start [shape=Mdiamond]
		boss [
			shape=house,
			stack.child_dotfile="%s",
			stack.child.var.x="pr=$context.pr_number",
			manager.poll_interval="10ms",
			manager.max_cycles=200
		]
		done [shape=Msquare]
		start -> boss -> done
	}`, childPath)

	be := fake.New()
	out, _, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusFail {
		t.Fatalf("expected FAIL when override references an unresolved key, got %s", out.Status)
	}
	if !strings.Contains(out.FailureReason, "pr_number") {
		t.Fatalf("failure reason should name the unresolved key: %q", out.FailureReason)
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
