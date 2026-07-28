package attractor_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
	graphpkg "github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/lint"
)

// reviewPipelineSrc reads the shipped review pipeline (items-spec I5).
func reviewPipelineSrc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../pipelines/review/pipeline.dot")
	must(t, err)
	return string(b)
}

func buildReviewGraph(t *testing.T) *graphpkg.Graph {
	t.Helper()
	return build(t, reviewPipelineSrc(t))
}

// TestReviewPipeline_LintsClean confirms the shipped review pipeline is
// structurally valid: it parses, builds, and lints with no ERROR.
func TestReviewPipeline_LintsClean(t *testing.T) {
	g := buildReviewGraph(t)
	diags, err := lint.ValidateOrError(g)
	if err != nil {
		t.Fatalf("review pipeline rejected: %v", err)
	}
	for _, d := range diags {
		if d.Severity == lint.Error {
			t.Fatalf("unexpected ERROR: %+v", d)
		}
	}
}

// TestReviewPipeline_CheckoutThenReviewLoop pins the RV3 contract: the
// first stage is a deterministic tool node running `gh pr checkout`, it
// gates a `stack.manager_loop` on success, that loop runs the shared
// review-core sub-pipeline seeding diff_cmd = `gh pr diff …`, and the loop
// flows to exit. The single-agent review node is gone.
func TestReviewPipeline_CheckoutThenReviewLoop(t *testing.T) {
	g := buildReviewGraph(t)

	out := g.OutgoingEdges("start")
	if len(out) != 1 {
		t.Fatalf("start should have exactly one outgoing edge, got %d", len(out))
	}
	checkoutID := out[0].To
	checkout := g.Nodes[checkoutID]
	if checkout.Type() != "tool" {
		t.Fatalf("first stage %q type=%q, want tool", checkoutID, checkout.Type())
	}
	if !strings.Contains(checkout.Attrs["tool_command"], "gh pr checkout") {
		t.Fatalf("checkout tool_command=%q, want it to run `gh pr checkout`", checkout.Attrs["tool_command"])
	}

	// checkout -> review_loop, gated on tool success.
	co := g.OutgoingEdges(checkoutID)
	if len(co) != 1 {
		t.Fatalf("checkout should have exactly one outgoing edge, got %d", len(co))
	}
	if cond := co[0].Attrs["condition"]; !strings.Contains(cond, "outcome=success") {
		t.Fatalf("checkout->review_loop condition=%q, want it gated on outcome=success", cond)
	}
	loopID := co[0].To
	loop := g.Nodes[loopID]
	if loop.Type() != "stack.manager_loop" {
		t.Fatalf("review stage %q type=%q, want stack.manager_loop", loopID, loop.Type())
	}
	if child := loop.Attrs["stack.child_dotfile"]; !strings.HasSuffix(child, "review-core/pipeline.dot") {
		t.Fatalf("review_loop child_dotfile=%q, want suffix review-core/pipeline.dot", child)
	}
	diffCmd := loop.Attrs["stack.child.var.diff_cmd"]
	for _, want := range []string{"gh pr diff", "$context.pr_number", "$context.repo"} {
		if !strings.Contains(diffCmd, want) {
			t.Fatalf("review_loop diff_cmd=%q, want it to contain %q", diffCmd, want)
		}
	}

	// review_loop -> exit.
	ro := g.OutgoingEdges(loopID)
	if len(ro) != 1 {
		t.Fatalf("review_loop should have exactly one outgoing edge, got %d", len(ro))
	}
	if dst := g.Nodes[ro[0].To]; dst.Type() != "exit" {
		t.Fatalf("review_loop flows to %q (type %q), want exit", ro[0].To, dst.Type())
	}

	// The single-agent review node is gone: no codergen stage remains.
	for id, n := range g.Nodes {
		if n.Type() == "codergen" {
			t.Fatalf("node %q is a codergen stage; the single-agent review path should be gone", id)
		}
	}
}

// TestReviewPipeline_ExpandsItemVars confirms the checkout command wires
// the item's `repo`/`pr_number` vars — the exact keys a GitHub PR Item
// supplies (internal/source/github.go) — into a concrete gh invocation.
// Post-C5 the pipeline uses `$context.` syntax resolved at runtime from
// the live context (spec §4.5), so this drives the same `Context.Expand`
// the tool handler calls, not the removed prepare-time transform.
func TestReviewPipeline_ExpandsItemVars(t *testing.T) {
	g := buildReviewGraph(t)
	checkout := g.Nodes[g.OutgoingEdges("start")[0].To]

	ctx := engine.NewContextFrom(map[string]string{
		"repo":      "owner/repo",
		"pr_number": "42",
	})
	got, err := ctx.Expand(checkout.Attrs["tool_command"])
	must(t, err)
	if got != "gh pr checkout 42 --repo owner/repo" {
		t.Fatalf("expanded checkout command=%q", got)
	}
}

// TestReviewPipeline_RoutesThroughLenses drives the review pipeline end to
// end (RV3): checkout runs, then review_loop runs the shared review-core
// sub-pipeline inline, fanning out to all five lenses before synth reports
// the verdict. The child_dotfile is the real review-core pipeline via an
// absolute path (the shipped relative path resolves against the pipeline's
// own dir, not the test cwd). A stub `gh` lets the checkout tool succeed.
func TestReviewPipeline_RoutesThroughLenses(t *testing.T) {
	// Stub `gh` so the deterministic checkout stage succeeds without a
	// network or a real repo.
	binDir := t.TempDir()
	stub := "#!/bin/sh\nexit 0\n"
	must(t, os.WriteFile(filepath.Join(binDir, "gh"), []byte(stub), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	childPath, err := filepath.Abs("../pipelines/review-core/pipeline.dot")
	must(t, err)
	src := fmt.Sprintf(`digraph review {
		start [shape=Mdiamond]
		checkout [type="tool",
		          tool_command="gh pr checkout $context.pr_number --repo $context.repo"]
		review_loop [type="stack.manager_loop",
		             stack.child_dotfile="%s",
		             stack.child.var.diff_cmd="gh pr diff $context.pr_number --repo $context.repo",
		             manager.poll_interval="10ms",
		             manager.max_cycles=2000]
		done [shape=Msquare]
		start -> checkout
		checkout -> review_loop [condition="outcome=success"]
		review_loop -> done
	}`, childPath)

	be := fake.New()
	for _, lens := range reviewCoreLenses {
		be.SetText(lens, "finding from "+lens)
	}
	be.SetSequence("synth", fake.Step{Outcome: &engine.Outcome{
		Status: engine.StatusSuccess,
		Notes:  "merged review",
		ContextUpdates: map[string]string{
			"review.summary": "all lenses clear",
			"review.verdict": "pass",
		},
	}})

	out, _, _ := runFixtureSeeded(t, src, be, nil, map[string]string{
		"repo":      "owner/repo",
		"pr_number": "42",
		"url":       "https://github.com/owner/repo/pull/42",
		"title":     "Fix login",
	})
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q", out.Status, out.FailureReason)
	}

	// The PR routed through every lens exactly once, and synth ran.
	for _, lens := range reviewCoreLenses {
		if n := be.CallCount(lens); n != 1 {
			t.Fatalf("lens %q called %d times, want 1", lens, n)
		}
	}
	if n := be.CallCount("synth"); n != 1 {
		t.Fatalf("synth called %d times, want 1", n)
	}

	// The seeded diff_cmd override resolved $context.pr_number/$context.repo
	// against the parent run's context before the child saw it.
	if got := childPrompt(t, be, "correctness"); !strings.Contains(got, "gh pr diff 42 --repo owner/repo") {
		t.Fatalf("lens prompt = %q, want it to contain resolved diff command", got)
	}
}
