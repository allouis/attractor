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

// TestRouterPipeline_RoutesItemTypeToManagerLoops pins the R6 contract:
// a conditional `classify` node routes on the seeded `item.type` to a
// per-type `stack.manager_loop` target, each carrying its own child
// pipeline. `pr` -> review child, `issue` -> implement child.
func TestRouterPipeline_RoutesItemTypeToManagerLoops(t *testing.T) {
	g := buildRouterGraph(t)

	// start -> classify (a conditional node).
	out := g.OutgoingEdges("start")
	if len(out) != 1 {
		t.Fatalf("start should have exactly one outgoing edge, got %d", len(out))
	}
	classifyID := out[0].To
	if got := g.Nodes[classifyID].Type(); got != "conditional" {
		t.Fatalf("classify node %q type=%q, want conditional", classifyID, got)
	}

	cases := []struct {
		itemType    string
		childSuffix string
	}{
		{"item.type=pr", "review/pipeline.dot"},
		{"item.type=issue", "implement/pipeline.dot"},
	}
	for _, c := range cases {
		target := condEdgeTo(t, g, classifyID, c.itemType)
		n := g.Nodes[target]
		if n.Type() != "stack.manager_loop" {
			t.Fatalf("%s target %q type=%q, want stack.manager_loop", c.itemType, target, n.Type())
		}
		child := n.Attrs["stack.child_dotfile"]
		if !strings.HasSuffix(child, c.childSuffix) {
			t.Fatalf("%s target child_dotfile=%q, want suffix %q", c.itemType, child, c.childSuffix)
		}
	}
}

// TestRouterPipeline_TriageFallback confirms the ambiguous path: an
// unknown item type routes to an agent `triage` node (a prompt-bearing
// codergen stage) whose emitted `decision` label routes to the same
// closed set of targets, plus a `needs_design` human surface.
func TestRouterPipeline_TriageFallback(t *testing.T) {
	g := buildRouterGraph(t)

	classifyID := g.OutgoingEdges("start")[0].To
	triageID := condEdgeTo(t, g, classifyID, "item.type=unknown")

	triage := g.Nodes[triageID]
	if triage.Type() != "codergen" {
		t.Fatalf("triage node %q type=%q, want codergen (agent fallback)", triageID, triage.Type())
	}
	if triage.Prompt() == "" {
		t.Fatalf("triage node %q has no prompt", triageID)
	}

	// The agent's `decision` label routes within the closed target set.
	reviewTarget := condEdgeTo(t, g, classifyID, "item.type=pr")
	implementTarget := condEdgeTo(t, g, classifyID, "item.type=issue")
	if got := condEdgeTo(t, g, triageID, "decision=review"); got != reviewTarget {
		t.Fatalf("triage decision=review -> %q, want %q (same review target as classify)", got, reviewTarget)
	}
	if got := condEdgeTo(t, g, triageID, "decision=implement"); got != implementTarget {
		t.Fatalf("triage decision=implement -> %q, want %q (same implement target)", got, implementTarget)
	}
	designTarget := condEdgeTo(t, g, triageID, "decision=design")
	if g.Nodes[designTarget].Type() != "tool" {
		t.Fatalf("triage decision=design -> %q type=%q, want tool (human surface)", designTarget, g.Nodes[designTarget].Type())
	}
}
