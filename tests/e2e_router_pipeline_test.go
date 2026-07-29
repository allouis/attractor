package attractor_test

import (
	"os"
	"strings"
	"testing"

	graphpkg "github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/lint"
)

// routerPipelineSrc reads the shipped router pipeline (router-spec R6).
func routerPipelineSrc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../pipelines/router/pipeline.dot")
	must(t, err)
	return string(b)
}

func buildRouterGraph(t *testing.T) *graphpkg.Graph {
	t.Helper()
	return build(t, routerPipelineSrc(t))
}

// TestRouterPipeline_LintsClean confirms the shipped router pipeline is
// structurally valid: it parses, builds, and lints with no ERROR.
func TestRouterPipeline_LintsClean(t *testing.T) {
	g := buildRouterGraph(t)
	diags, err := lint.ValidateOrError(g)
	if err != nil {
		t.Fatalf("router pipeline rejected: %v", err)
	}
	for _, d := range diags {
		if d.Severity == lint.Error {
			t.Fatalf("unexpected ERROR: %+v", d)
		}
	}
}

// condEdgeTo returns the destination node ID of the edge out of `from`
// whose condition matches `cond`, failing if there is no such edge.
func condEdgeTo(t *testing.T, g *graphpkg.Graph, from, cond string) string {
	t.Helper()
	for _, e := range g.OutgoingEdges(from) {
		if strings.TrimSpace(e.Condition()) == cond {
			return e.To
		}
	}
	t.Fatalf("node %q has no outgoing edge with condition %q", from, cond)
	return ""
}

// managerLoopChild asserts `target` is a stack.manager_loop and returns its
// child_dotfile.
func managerLoopChild(t *testing.T, g *graphpkg.Graph, target string) string {
	t.Helper()
	n := g.Nodes[target]
	if n.Type() != "stack.manager_loop" {
		t.Fatalf("target %q type=%q, want stack.manager_loop", target, n.Type())
	}
	return n.Attrs["stack.child_dotfile"]
}

// TestRouterPipeline_RoutesItemTypeToManagerLoops pins the R6 contract: the
// conditional `classify` routes a PR deterministically to the review-pr
// child, and sends an issue (or unknown type) to the `triage` agent.
func TestRouterPipeline_RoutesItemTypeToManagerLoops(t *testing.T) {
	g := buildRouterGraph(t)

	classifyID := g.OutgoingEdges("start")[0].To
	if got := g.Nodes[classifyID].Type(); got != "conditional" {
		t.Fatalf("classify node %q type=%q, want conditional", classifyID, got)
	}

	// pr -> review-pr manager_loop (deterministic).
	if child := managerLoopChild(t, g, condEdgeTo(t, g, classifyID, "item.type=pr")); !strings.HasSuffix(child, "review-pr/pipeline.dot") {
		t.Fatalf("pr target child=%q, want review-pr/pipeline.dot", child)
	}
	// issue and unknown both go to the triage agent (a codergen node).
	for _, cond := range []string{"item.type=issue", "item.type=unknown"} {
		target := condEdgeTo(t, g, classifyID, cond)
		if got := g.Nodes[target].Type(); got != "codergen" {
			t.Fatalf("%s target %q type=%q, want codergen (triage)", cond, target, got)
		}
	}
}

// TestRouterPipeline_TriageFallback confirms the triage agent's `decision`
// routes within the closed work-pipeline set: bugfix / implement / review
// each to their manager_loop child, and design to the human surface.
func TestRouterPipeline_TriageFallback(t *testing.T) {
	g := buildRouterGraph(t)

	classifyID := g.OutgoingEdges("start")[0].To
	triageID := condEdgeTo(t, g, classifyID, "item.type=issue")
	triage := g.Nodes[triageID]
	if triage.Type() != "codergen" || triage.Prompt() == "" {
		t.Fatalf("triage node %q type=%q prompt=%q, want a prompt-bearing codergen", triageID, triage.Type(), triage.Prompt())
	}

	for decision, childSuffix := range map[string]string{
		"decision=review":    "review-pr/pipeline.dot",
		"decision=implement": "implement/pipeline.dot",
		"decision=bugfix":    "bug-fix/pipeline.dot",
	} {
		if child := managerLoopChild(t, g, condEdgeTo(t, g, triageID, decision)); !strings.HasSuffix(child, childSuffix) {
			t.Fatalf("triage %s child=%q, want %q", decision, child, childSuffix)
		}
	}
	designTarget := condEdgeTo(t, g, triageID, "decision=design")
	if g.Nodes[designTarget].Type() != "tool" {
		t.Fatalf("triage decision=design -> %q type=%q, want tool (human surface)", designTarget, g.Nodes[designTarget].Type())
	}
}
