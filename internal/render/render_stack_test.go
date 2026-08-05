package render

import (
	"os"
	"os/exec"
	"path/filepath"
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
	out, err := SVG([]byte(src), "")
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

// TestGraphvizSafe_ShapesByType asserts the renderer picks a distinct
// shape from each node's resolved type, so the graph distinguishes roles
// even when pipelines set type= explicitly with a default box shape.
func TestGraphvizSafe_ShapesByType(t *testing.T) {
	src := `digraph p {
		start [shape=Mdiamond]
		gen  [type="codergen.acp", prompt="x"]
		run  [type="tool", tool_command="x"]
		loop [type="stack.manager_loop", stack.child_dotfile="c.dot"]
		gate [type="conditional"]
		done [shape=Msquare]
		start -> gen -> run -> loop -> gate -> done
	}`
	out := string(graphvizSafe([]byte(src)))
	for node, shape := range map[string]string{
		"start": "Mdiamond", "gen": "box", "run": "parallelogram",
		"loop": "box3d", "gate": "diamond", "done": "Msquare",
	} {
		if want := `"` + node + `" [shape="` + shape + `"`; !strings.Contains(out, want) {
			t.Errorf("node %q: want shape %q; DOT:\n%s", node, shape, out)
		}
	}
}

// TestDotArgs_EngineFlag asserts the graphviz command line carries -K<engine>
// only for a non-empty engine, so the default (empty) keeps the historical
// `dot -Tsvg` invocation and a chosen engine selects the layout algorithm.
func TestDotArgs_EngineFlag(t *testing.T) {
	if got := dotArgs(""); len(got) != 1 || got[0] != "-Tsvg" {
		t.Errorf("dotArgs(\"\") = %v, want [-Tsvg]", got)
	}
	if got := dotArgs("neato"); len(got) != 2 || got[0] != "-Kneato" || got[1] != "-Tsvg" {
		t.Errorf("dotArgs(\"neato\") = %v, want [-Kneato -Tsvg]", got)
	}
}

// TestValidEngine gates the allowlist the graph endpoints screen `?engine=`
// against, so no arbitrary flag reaches the exec'd `dot`.
func TestValidEngine(t *testing.T) {
	for _, e := range []string{"dot", "neato", "fdp", "sfdp", "circo", "twopi"} {
		if !ValidEngine(e) {
			t.Errorf("ValidEngine(%q) = false, want true", e)
		}
	}
	for _, e := range []string{"", "bogus", "-Tpng", "dot; rm"} {
		if ValidEngine(e) {
			t.Errorf("ValidEngine(%q) = true, want false", e)
		}
	}
}

// TestGraphHeader_AestheticDefaults asserts the shared graph header sets the
// node/edge/graph attributes that make the run graph look polished
// (ui-tailwind-spec T7a): rounded filled nodes at the UI font, right-sized
// arrowheads on smooth splines, and tidy rank/node spacing. Both the flat
// (graphvizSafe) and expanded (graphvizExpanded) emitters go through the same
// header, so both must carry the defaults; the exact values may be tuned by the
// visual review, so this guards the attributes are set, not their magnitudes.
func TestGraphHeader_AestheticDefaults(t *testing.T) {
	src := `digraph p {
		start [shape=Mdiamond]
		gen  [type="codergen.acp", prompt="x"]
		done [shape=Msquare]
		start -> gen -> done
	}`
	// graph-level layout attributes
	graphAttrs := []string{"nodesep=", "ranksep=", "splines="}
	// node defaults: rounded fill at the UI font
	nodeAttrs := []string{"fontname=", "fontsize=", `style="rounded`}
	// edge defaults: right-sized arrowheads
	edgeAttrs := []string{"arrowsize="}

	safe := string(graphvizSafe([]byte(src)))
	assertHeaderAttrs(t, "graphvizSafe", safe, graphAttrs, nodeAttrs, edgeAttrs)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "child.dot"), []byte(`digraph c {
		start [shape=Mdiamond]
		worka [prompt="a"]
		done [shape=Msquare]
		start -> worka -> done
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := `digraph parent {
		start [shape=Mdiamond]
		loop [type="stack.manager_loop", stack.child_dotfile="child.dot"]
		done [shape=Msquare]
		start -> loop -> done
	}`
	expanded := string(graphvizExpanded([]byte(parent), dir))
	assertHeaderAttrs(t, "graphvizExpanded", expanded, graphAttrs, nodeAttrs, edgeAttrs)
	// The expanded emitter must keep its cluster-clipping directive.
	if !strings.Contains(expanded, "compound=true") {
		t.Errorf("graphvizExpanded dropped compound=true:\n%s", expanded)
	}
}

// assertHeaderAttrs checks every graph/node/edge default appears in out, and
// that the node/edge defaults ride on graphviz default statements (node [ … ],
// edge [ … ]) rather than a per-node override.
func assertHeaderAttrs(t *testing.T, name, out string, graphAttrs, nodeAttrs, edgeAttrs []string) {
	t.Helper()
	if !strings.Contains(out, "node [") {
		t.Errorf("%s missing a node-default statement (node [ … ]):\n%s", name, out)
	}
	if !strings.Contains(out, "edge [") {
		t.Errorf("%s missing an edge-default statement (edge [ … ]):\n%s", name, out)
	}
	for _, want := range append(append(append([]string{}, graphAttrs...), nodeAttrs...), edgeAttrs...) {
		if !strings.Contains(out, want) {
			t.Errorf("%s missing DOT attribute %q:\n%s", name, want, out)
		}
	}
}

// TestGraphvizExpanded_InlinesChild verifies a stack.manager_loop node is
// expanded into a cluster containing the (namespaced) child pipeline, with
// compound edges clipped at the cluster boundary.
func TestGraphvizExpanded_InlinesChild(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "child.dot"), []byte(`digraph child {
		start [shape=Mdiamond]
		worka [prompt="a"]
		done [shape=Msquare]
		start -> worka -> done
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := `digraph parent {
		start [shape=Mdiamond]
		loop [type="stack.manager_loop", stack.child_dotfile="child.dot"]
		done [shape=Msquare]
		start -> loop -> done
	}`
	out := string(graphvizExpanded([]byte(parent), dir))
	for _, want := range []string{"subgraph cluster_loop", `"loop/worka"`, "lhead=", "ltail=", "compound=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("expanded DOT missing %q:\n%s", want, out)
		}
	}
	// The manager_loop node itself must not also appear as a plain node.
	if strings.Contains(out, `"loop" [shape`) {
		t.Errorf("expanded node also drawn as a plain node:\n%s", out)
	}
}
