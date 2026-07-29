package render

import (
	"os/exec"
	"strings"
	"testing"
)

// TestSVG_RendersDottedAttrPipeline ensures a pipeline using Attractor's
// dotted attribute names (e.g. stack.child_dotfile) renders — graphviz's
// own parser rejects such names, so SVG must re-emit clean DOT first.
func TestSVG_RendersDottedAttrPipeline(t *testing.T) {
	if _, err := exec.LookPath("dot"); err != nil {
		t.Skip("graphviz not installed")
	}
	src := `digraph review {
		start [shape=Mdiamond]
		review_loop [type="stack.manager_loop", stack.child_dotfile="../review-core/pipeline.dot", stack.child.var.diff_cmd="jj diff"]
		done [shape=Msquare]
		start -> review_loop [condition="outcome=success"]
		review_loop -> done
	}`
	out, err := SVG([]byte(src))
	if err != nil {
		t.Fatalf("SVG failed on dotted-attr pipeline: %v", err)
	}
	if !strings.Contains(string(out), "<svg") {
		t.Fatalf("output is not SVG: %.80s", out)
	}
	if !strings.Contains(string(out), "review_loop") {
		t.Fatalf("node label missing from SVG")
	}
}
