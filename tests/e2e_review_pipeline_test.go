package attractor_test

import (
	"os"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/dot"
	graphpkg "github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/lint"
	"github.com/allouis/attractor/internal/transform"
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

// TestReviewPipeline_CheckoutThenReviewShape pins the I5 contract: the
// first stage is a deterministic tool node running `gh pr checkout`, it
// gates a codergen review stage on success, and review flows to exit.
func TestReviewPipeline_CheckoutThenReviewShape(t *testing.T) {
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

	// checkout -> review, gated on tool success.
	co := g.OutgoingEdges(checkoutID)
	if len(co) != 1 {
		t.Fatalf("checkout should have exactly one outgoing edge, got %d", len(co))
	}
	if cond := co[0].Attrs["condition"]; !strings.Contains(cond, "outcome=success") {
		t.Fatalf("checkout->review condition=%q, want it gated on outcome=success", cond)
	}
	reviewID := co[0].To
	review := g.Nodes[reviewID]
	if review.Type() != "codergen" {
		t.Fatalf("review stage %q type=%q, want codergen", reviewID, review.Type())
	}
	if review.Prompt() == "" {
		t.Fatalf("review stage %q has no prompt", reviewID)
	}

	// review -> exit.
	ro := g.OutgoingEdges(reviewID)
	if len(ro) != 1 {
		t.Fatalf("review should have exactly one outgoing edge, got %d", len(ro))
	}
	if dst := g.Nodes[ro[0].To]; dst.Type() != "exit" {
		t.Fatalf("review flows to %q (type %q), want exit", ro[0].To, dst.Type())
	}
}

// TestReviewPipeline_ExpandsItemVars confirms the checkout command wires
// the item's `repo`/`pr_number` vars — the exact keys a GitHub PR Item
// supplies (internal/source/github.go) — into a concrete gh invocation.
func TestReviewPipeline_ExpandsItemVars(t *testing.T) {
	file, err := dot.Parse(reviewPipelineSrc(t))
	must(t, err)
	g, err := graphpkg.Build(file)
	must(t, err)
	g, err = transform.VariableExpansion{Vars: map[string]string{
		"repo":      "owner/repo",
		"pr_number": "42",
	}}.Apply(g)
	must(t, err)

	checkout := g.Nodes[g.OutgoingEdges("start")[0].To]
	got := checkout.Attrs["tool_command"]
	if got != "gh pr checkout 42 --repo owner/repo" {
		t.Fatalf("expanded checkout command=%q", got)
	}
}
