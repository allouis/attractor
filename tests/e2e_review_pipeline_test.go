package attractor_test

import (
	"os"
	"strings"
	"testing"

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
