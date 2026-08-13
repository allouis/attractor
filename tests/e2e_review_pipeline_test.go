package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/engine"
	graphpkg "github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/lint"
	"github.com/allouis/attractor/internal/setup"
)

// reviewPipelineSrc reads the shipped review pipeline (items-spec I5).
func reviewPipelineSrc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../pipelines/review-pr/pipeline.dot")
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

// TestReviewPipeline_CheckoutThenReviewLoop pins the RV3 contract, post
// checkout-drop and post-D6: the review is DIFF-BASED — the first stage
// is a `subgraph` node inlining the shared review-core pipeline with
// diff_cmd = `gh pr diff …` (no local checkout, works under any
// runner), flowing to exit. The single-agent review node is gone.
func TestReviewPipeline_CheckoutThenReviewLoop(t *testing.T) {
	g := buildReviewGraph(t)

	out := g.OutgoingEdges("start")
	if len(out) != 1 {
		t.Fatalf("start should have exactly one outgoing edge, got %d", len(out))
	}
	loopID := out[0].To
	loop := g.Nodes[loopID]
	if loop.Type() != "subgraph" {
		t.Fatalf("review stage %q type=%q, want subgraph (D6 static inline)", loopID, loop.Type())
	}
	if child := loop.Attrs["graph_ref"]; !strings.HasSuffix(child, "review-core/pipeline.dot") {
		t.Fatalf("review_loop graph_ref=%q, want suffix review-core/pipeline.dot", child)
	}
	diffCmd := loop.Attrs["var.diff_cmd"]
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
	// (A checkout tool node is gone too — the review is diff-based.)
	for id, n := range g.Nodes {
		if n.Type() == "codergen" {
			t.Fatalf("node %q is a codergen stage; the single-agent review path should be gone", id)
		}
		if n.Type() == "tool" {
			t.Fatalf("node %q is a tool stage; the checkout node should be gone (diff-based review)", id)
		}
	}
}

// TestReviewPipeline_ExpandsItemVars confirms the load-time expansion
// (D6) seeds the concrete diff command into the inlined lens prompts:
// var.diff_cmd substitutes statically, and its embedded `$context.`
// item keys (repo/pr_number, the exact keys a GitHub PR Item supplies)
// resolve at runtime from the live context (spec §4.5).
func TestReviewPipeline_ExpandsItemVars(t *testing.T) {
	baseDir, err := filepath.Abs("../pipelines/review-pr")
	must(t, err)
	prepared, err := setup.Prepare(setup.Options{
		Source:  reviewPipelineSrc(t),
		BaseDir: baseDir,
	})
	must(t, err)
	lens := prepared.Graph.Nodes["review_loop.correctness"]
	if lens == nil {
		t.Fatalf("review-core not inlined; nodes: %v", prepared.Graph.NodeOrder)
	}
	if !strings.Contains(lens.Attrs["prompt"], "gh pr diff $context.pr_number --repo $context.repo") {
		t.Fatalf("lens prompt not seeded with diff_cmd: %q", lens.Attrs["prompt"])
	}
	ctx := engine.NewContextFrom(map[string]string{
		"repo":      "owner/repo",
		"pr_number": "42",
	})
	got, err := ctx.Expand(lens.Attrs["prompt"])
	must(t, err)
	if !strings.Contains(got, "gh pr diff 42 --repo owner/repo") {
		t.Fatalf("runtime expansion wrong: %q", got)
	}
}
