package transform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/graph"
)

func buildG(t *testing.T, src string) *graph.Graph {
	t.Helper()
	f, err := dot.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g, err := graph.Build(f)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return g
}

// writeChild materializes a child pipeline plus a prompt file so the
// child's own PromptFile pass is exercised.
func writeChild(t *testing.T, dir string) string {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "prompts"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "prompts", "lens.md"), []byte("review via $context.diff_cmd"), 0o644))
	child := `digraph review_core {
		vars = "diff_cmd"
		start [shape=Mdiamond]
		fan_out [shape=component]
		lens_a [prompt="@prompts/lens.md", output_key="review.a"]
		lens_b [prompt="check $context.diff_cmd again"]
		synth [require_status="true", prompt="merge findings"]
		done [shape=Msquare]
		start -> fan_out
		fan_out -> lens_a
		fan_out -> lens_b
		lens_a -> synth
		lens_b -> synth
		synth -> done
	}`
	path := filepath.Join(dir, "child.dot")
	must(os.WriteFile(path, []byte(child), 0o644))
	return path
}

func parentSrc(childPath string) string {
	return `digraph parent {
		start [shape=Mdiamond]
		work [prompt="do the work"]
		review [type="subgraph", graph_ref="` + childPath + `",
		        var.diff_cmd="jj diff --from $context.review_base --to @"]
		fix [prompt="address findings"]
		ship [shape=Msquare]
		start -> work -> review
		review -> ship [condition="outcome=success"]
		review -> fix  [condition="outcome=fail"]
		fix -> review
	}`
}

func expand(t *testing.T, g *graph.Graph) *graph.Graph {
	t.Helper()
	out, err := Subgraph{}.Apply(g)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	return out
}

// D6: a subgraph node is replaced at load time by the child's nodes,
// IDs prefixed with the subgraph node's ID.
func TestSubgraph_InlinesChildNodes(t *testing.T) {
	dir := t.TempDir()
	g := expand(t, buildG(t, parentSrc(writeChild(t, dir))))

	if _, ok := g.Nodes["review"]; ok {
		t.Fatal("subgraph node must be replaced")
	}
	for _, id := range []string{"review.fan_out", "review.lens_a", "review.lens_b", "review.synth"} {
		if _, ok := g.Nodes[id]; !ok {
			t.Fatalf("missing inlined node %q; have %v", id, g.NodeOrder)
		}
	}
	// The child's own start/exit disappear — they are splice points.
	for _, id := range []string{"review.start", "review.done"} {
		if _, ok := g.Nodes[id]; ok {
			t.Fatalf("child terminal node %q must not survive", id)
		}
	}
}

// The child's own PromptFile pass runs first (relative to the child's
// dir), and var.* seeding substitutes statically — the value itself may
// carry $context.* refs that resolve at runtime against the parent run.
func TestSubgraph_PromptsAndVarSeeding(t *testing.T) {
	dir := t.TempDir()
	g := expand(t, buildG(t, parentSrc(writeChild(t, dir))))

	a := g.Nodes["review.lens_a"].Attrs["prompt"]
	if !strings.Contains(a, "review via jj diff --from $context.review_base --to @") {
		t.Fatalf("lens_a prompt not expanded+seeded: %q", a)
	}
	b := g.Nodes["review.lens_b"].Attrs["prompt"]
	if !strings.Contains(b, "check jj diff --from $context.review_base --to @ again") {
		t.Fatalf("lens_b prompt not seeded: %q", b)
	}
}

// Parent edges splice through the child's start/exit: incoming edges
// land on the child start's successors; the child's pre-exit nodes take
// the parent's outgoing edges (their outcome drives the conditions).
func TestSubgraph_EdgeSplicing(t *testing.T) {
	dir := t.TempDir()
	g := expand(t, buildG(t, parentSrc(writeChild(t, dir))))

	find := func(from, to string) *graph.Edge {
		for _, e := range g.Edges {
			if e.From == from && e.To == to {
				return e
			}
		}
		return nil
	}
	if find("work", "review.fan_out") == nil {
		t.Fatalf("incoming edge not spliced to child entry; edges: %v", edgeList(g))
	}
	if e := find("review.synth", "ship"); e == nil || e.Attrs["condition"] != "outcome=success" {
		t.Fatalf("outgoing success edge not spliced from pre-exit node; edges: %v", edgeList(g))
	}
	if e := find("review.synth", "fix"); e == nil || e.Attrs["condition"] != "outcome=fail" {
		t.Fatalf("outgoing fail edge not spliced; edges: %v", edgeList(g))
	}
	if find("fix", "review.fan_out") == nil {
		t.Fatalf("revisit edge not spliced to child entry; edges: %v", edgeList(g))
	}
	// No edge may still reference the removed subgraph node.
	for _, e := range g.Edges {
		if e.From == "review" || e.To == "review" {
			t.Fatalf("dangling edge to removed subgraph node: %+v", e)
		}
	}
}

// A graph_ref that does not exist fails loudly at load time.
func TestSubgraph_MissingChildErrors(t *testing.T) {
	g := buildG(t, `digraph p {
		start [shape=Mdiamond]
		sub [type="subgraph", graph_ref="/nope/child.dot"]
		done [shape=Msquare]
		start -> sub -> done
	}`)
	if _, err := (Subgraph{}).Apply(g); err == nil {
		t.Fatal("want error for missing graph_ref")
	}
}

// Nested subgraphs expand recursively, bounded against cycles.
func TestSubgraph_NestedExpansion(t *testing.T) {
	dir := t.TempDir()
	inner := filepath.Join(dir, "inner.dot")
	if err := os.WriteFile(inner, []byte(`digraph inner {
		start [shape=Mdiamond]
		leaf [prompt="leaf work"]
		done [shape=Msquare]
		start -> leaf -> done
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mid := filepath.Join(dir, "mid.dot")
	if err := os.WriteFile(mid, []byte(`digraph mid {
		start [shape=Mdiamond]
		sub [type="subgraph", graph_ref="inner.dot"]
		done [shape=Msquare]
		start -> sub -> done
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	g := buildG(t, `digraph outer {
		start [shape=Mdiamond]
		wrap [type="subgraph", graph_ref="`+mid+`"]
		done [shape=Msquare]
		start -> wrap -> done
	}`)
	out := expand(t, g)
	if _, ok := out.Nodes["wrap.sub.leaf"]; !ok {
		t.Fatalf("nested child not expanded; nodes: %v", out.NodeOrder)
	}
}

// A subgraph that references itself terminates with an error instead of
// recursing forever.
func TestSubgraph_SelfReferenceErrors(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "self.dot")
	if err := os.WriteFile(self, []byte(`digraph self {
		start [shape=Mdiamond]
		again [type="subgraph", graph_ref="self.dot"]
		done [shape=Msquare]
		start -> again -> done
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	g := buildG(t, `digraph outer {
		start [shape=Mdiamond]
		sub [type="subgraph", graph_ref="`+self+`"]
		done [shape=Msquare]
		start -> sub -> done
	}`)
	if _, err := (Subgraph{}).Apply(g); err == nil {
		t.Fatal("want error for recursive graph_ref")
	}
}

func edgeList(g *graph.Graph) []string {
	var out []string
	for _, e := range g.Edges {
		out = append(out, e.From+"->"+e.To)
	}
	return out
}

// Audit bug 2: var substitution used plain strings.ReplaceAll, so
// seeding var.foo also corrupted $context.foobar (→ <val>bar),
// nondeterministically by map order. Substitution must be
// identifier-boundary aware.
func TestSubgraph_VarSubstitutionRespectsIdentifierBoundaries(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child.dot")
	if err := os.WriteFile(child, []byte(`digraph c {
		vars = "foo"
		start [shape=Mdiamond]
		work [prompt="use $context.foo but keep $context.foobar and $context.foo.nested"]
		done [shape=Msquare]
		start -> work -> done
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	g := buildG(t, `digraph p {
		start [shape=Mdiamond]
		sub [type="subgraph", graph_ref="`+child+`", var.foo="VAL"]
		done [shape=Msquare]
		start -> sub -> done
	}`)
	out := expand(t, g)
	prompt := out.Nodes["sub.work"].Attrs["prompt"]
	if !strings.Contains(prompt, "use VAL but") {
		t.Fatalf("$context.foo not substituted: %q", prompt)
	}
	if !strings.Contains(prompt, "$context.foobar") {
		t.Fatalf("$context.foobar corrupted by prefix substitution: %q", prompt)
	}
	if !strings.Contains(prompt, "$context.foo.nested") {
		t.Fatalf("$context.foo.nested corrupted (dotted path shares the identifier): %q", prompt)
	}
}
