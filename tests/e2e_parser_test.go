package attractor_test

import (
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/dot"
)

func TestParse_LinearPipeline(t *testing.T) {
	src := `
digraph linear {
    start [shape=Mdiamond]
    plan  [prompt="Plan the change"]
    done  [shape=Msquare]

    start -> plan
    plan  -> done
}`
	file, err := dot.Parse(src)
	must(t, err)
	if file.Name != "linear" {
		t.Fatalf("graph name = %q, want %q", file.Name, "linear")
	}
	nodes := collectNodes(file.Statements)
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes, want 3 (%v)", len(nodes), nodeIDs(nodes))
	}
	edges := collectEdges(file.Statements)
	if len(edges) != 2 {
		t.Fatalf("got %d edges, want 2", len(edges))
	}
	if edges[0].From != "start" || edges[0].To != "plan" {
		t.Fatalf("edge[0] = %v", edges[0])
	}
}

func TestParse_GraphLevelAttributes(t *testing.T) {
	src := `
digraph g {
    goal = "Implement feature X"
    label = "Pipeline"
    default_max_retries = 3
    start [shape=Mdiamond]
    done  [shape=Msquare]
    start -> done
}`
	file, err := dot.Parse(src)
	must(t, err)
	got := map[string]string{}
	for _, s := range file.Statements {
		if d, ok := s.(dot.DeclStmt); ok {
			got[d.Key] = d.Value
		}
		if g, ok := s.(dot.GraphAttrStmt); ok {
			for k, v := range g.Attrs {
				got[k] = v
			}
		}
	}
	if got["goal"] != "Implement feature X" {
		t.Fatalf("goal = %q", got["goal"])
	}
	if got["default_max_retries"] != "3" {
		t.Fatalf("default_max_retries = %q", got["default_max_retries"])
	}
}

func TestParse_GraphAttrBlock(t *testing.T) {
	src := `digraph g {
    graph [goal="x", label="y"]
    start [shape=Mdiamond]
    done [shape=Msquare]
    start -> done
}`
	file, err := dot.Parse(src)
	must(t, err)
	var attrs map[string]string
	for _, s := range file.Statements {
		if g, ok := s.(dot.GraphAttrStmt); ok {
			attrs = g.Attrs
		}
	}
	if attrs == nil {
		t.Fatal("no GraphAttrStmt produced")
	}
	if attrs["goal"] != "x" || attrs["label"] != "y" {
		t.Fatalf("attrs = %v", attrs)
	}
}

func TestParse_MultilineAttrBlock(t *testing.T) {
	src := `digraph g {
    start [shape=Mdiamond]
    plan [
        prompt="multi
line
prompt",
        max_retries=2,
        goal_gate=true
    ]
    done [shape=Msquare]
    start -> plan -> done
}`
	file, err := dot.Parse(src)
	must(t, err)
	plan := findNode(t, file, "plan")
	if !strings.Contains(plan.Attrs["prompt"], "multi") {
		t.Fatalf("prompt = %q", plan.Attrs["prompt"])
	}
	if plan.Attrs["max_retries"] != "2" {
		t.Fatalf("max_retries = %q", plan.Attrs["max_retries"])
	}
	if plan.Attrs["goal_gate"] != "true" {
		t.Fatalf("goal_gate = %q", plan.Attrs["goal_gate"])
	}
}

func TestParse_ChainedEdges(t *testing.T) {
	src := `digraph g {
    start [shape=Mdiamond]
    done  [shape=Msquare]
    start -> a -> b -> done [label="next"]
}`
	file, err := dot.Parse(src)
	must(t, err)
	edges := collectEdges(file.Statements)
	if len(edges) != 3 {
		t.Fatalf("got %d edges, want 3", len(edges))
	}
	for _, e := range edges {
		if e.Attrs["label"] != "next" {
			t.Fatalf("edge %s->%s missing label", e.From, e.To)
		}
	}
}

func TestParse_NodeAndEdgeDefaults(t *testing.T) {
	src := `digraph g {
    node [class="planning"]
    edge [weight=1]
    start [shape=Mdiamond]
    plan
    done [shape=Msquare]
    start -> plan
    plan -> done
}`
	file, err := dot.Parse(src)
	must(t, err)
	var sawNodeDefault, sawEdgeDefault bool
	for _, s := range file.Statements {
		if _, ok := s.(dot.NodeDefaults); ok {
			sawNodeDefault = true
		}
		if _, ok := s.(dot.EdgeDefaults); ok {
			sawEdgeDefault = true
		}
	}
	if !sawNodeDefault || !sawEdgeDefault {
		t.Fatalf("missing defaults blocks: node=%v edge=%v", sawNodeDefault, sawEdgeDefault)
	}
}

func TestParse_Subgraph(t *testing.T) {
	src := `digraph g {
    start [shape=Mdiamond]
    done  [shape=Msquare]
    subgraph cluster_critique {
        node [type="codergen.openai"]
        review_a [prompt="critique a"]
        review_b [prompt="critique b"]
    }
    start -> review_a -> review_b -> done
}`
	file, err := dot.Parse(src)
	must(t, err)
	var sg *dot.SubgraphStmt
	for i := range file.Statements {
		if s, ok := file.Statements[i].(dot.SubgraphStmt); ok {
			sg = &s
			break
		}
	}
	if sg == nil {
		t.Fatal("expected a subgraph statement")
	}
	if sg.Name != "cluster_critique" {
		t.Fatalf("subgraph name = %q", sg.Name)
	}
	innerNodes := collectNodes(sg.Statements)
	if len(innerNodes) != 2 {
		t.Fatalf("subgraph nodes = %d", len(innerNodes))
	}
}

func TestParse_CommentsStripped(t *testing.T) {
	src := `// header comment
digraph g {
    /* inline
       block comment */
    start [shape=Mdiamond]  // trailing
    done [shape=Msquare]
    start -> done
}`
	_, err := dot.Parse(src)
	must(t, err)
}

func TestParse_QuotedStringEscapes(t *testing.T) {
	src := `digraph g {
    start [shape=Mdiamond]
    a [prompt="line1\nline2\t\"quoted\""]
    done [shape=Msquare]
    start -> a -> done
}`
	file, err := dot.Parse(src)
	must(t, err)
	a := findNode(t, file, "a")
	if a.Attrs["prompt"] != "line1\nline2\t\"quoted\"" {
		t.Fatalf("prompt = %q", a.Attrs["prompt"])
	}
}

func TestParse_QualifiedAttrKey(t *testing.T) {
	src := `digraph g {
    start [shape=Mdiamond]
    boss [type="subgraph", graph_ref="child.dot", var.diff_cmd="jj diff"]
    done [shape=Msquare]
    start -> boss -> done
}`
	file, err := dot.Parse(src)
	must(t, err)
	boss := findNode(t, file, "boss")
	if boss.Attrs["graph_ref"] != "child.dot" {
		t.Fatalf("graph_ref = %q", boss.Attrs["graph_ref"])
	}
	if boss.Attrs["var.diff_cmd"] != "jj diff" {
		t.Fatalf("var.diff_cmd = %q", boss.Attrs["var.diff_cmd"])
	}
}

func TestParse_BareValueTypes(t *testing.T) {
	src := `digraph g {
    start [shape=Mdiamond]
    a [shape=box, goal_gate=true, max_retries=4, timeout=15m, reasoning_effort=high]
    done [shape=Msquare]
    start -> a -> done
}`
	file, err := dot.Parse(src)
	must(t, err)
	a := findNode(t, file, "a")
	want := map[string]string{
		"shape":            "box",
		"goal_gate":        "true",
		"max_retries":      "4",
		"timeout":          "15m",
		"reasoning_effort": "high",
	}
	for k, v := range want {
		if a.Attrs[k] != v {
			t.Fatalf("attr %s = %q, want %q", k, a.Attrs[k], v)
		}
	}
}

func TestParse_RejectStrict(t *testing.T) {
	_, err := dot.Parse(`strict digraph g { start [shape=Mdiamond] done [shape=Msquare] start -> done }`)
	if err == nil || !strings.Contains(err.Error(), "strict") {
		t.Fatalf("expected `strict` rejection, got %v", err)
	}
}

func TestParse_RejectUndirected(t *testing.T) {
	_, err := dot.Parse(`graph g { a -- b }`)
	if err == nil || !strings.Contains(err.Error(), "undirected") {
		t.Fatalf("expected undirected rejection, got %v", err)
	}
}

func TestParse_RejectDoubleDash(t *testing.T) {
	_, err := dot.Parse(`digraph g { a -- b }`)
	if err == nil || !strings.Contains(err.Error(), "undirected") {
		t.Fatalf("expected `--` rejection, got %v", err)
	}
}

func TestParse_RejectMultiGraph(t *testing.T) {
	src := `
digraph g1 { start [shape=Mdiamond] done [shape=Msquare] start -> done }
digraph g2 { start [shape=Mdiamond] done [shape=Msquare] start -> done }`
	_, err := dot.Parse(src)
	if err == nil {
		t.Fatal("expected multi-graph rejection")
	}
}

// ---------- helpers ----------

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func collectNodes(stmts []dot.Statement) []dot.NodeStmt {
	var out []dot.NodeStmt
	for _, s := range stmts {
		if n, ok := s.(dot.NodeStmt); ok {
			out = append(out, n)
		}
		if g, ok := s.(dot.SubgraphStmt); ok {
			out = append(out, collectNodes(g.Statements)...)
		}
	}
	return out
}

func collectEdges(stmts []dot.Statement) []dot.EdgeStmt {
	var out []dot.EdgeStmt
	for _, s := range stmts {
		out = append(out, dot.EdgesOf(s)...)
		if g, ok := s.(dot.SubgraphStmt); ok {
			out = append(out, collectEdges(g.Statements)...)
		}
	}
	return out
}

func findNode(t *testing.T, file *dot.File, id string) dot.NodeStmt {
	t.Helper()
	for _, n := range collectNodes(file.Statements) {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("node %q not found", id)
	return dot.NodeStmt{}
}

func nodeIDs(nodes []dot.NodeStmt) []string {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	return ids
}
