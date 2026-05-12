package attractor_test

import (
	"testing"
	"time"

	"github.com/fabro/attractor/internal/dot"
	"github.com/fabro/attractor/internal/graph"
	"github.com/fabro/attractor/internal/transform"
)

func buildGraph(t *testing.T, src string) *graph.Graph {
	t.Helper()
	file, err := dot.Parse(src)
	must(t, err)
	g, err := graph.Build(file)
	must(t, err)
	return g
}

func TestBuild_NodeOrder(t *testing.T) {
	g := buildGraph(t, `digraph g {
		start [shape=Mdiamond]
		a [prompt="A"]
		b [prompt="B"]
		done [shape=Msquare]
		start -> a -> b -> done
	}`)
	want := []string{"start", "a", "b", "done"}
	for i, id := range want {
		if g.NodeOrder[i] != id {
			t.Fatalf("NodeOrder[%d]=%q, want %q", i, g.NodeOrder[i], id)
		}
	}
}

func TestBuild_NodeDefaultsApplied(t *testing.T) {
	g := buildGraph(t, `digraph g {
		start [shape=Mdiamond]
		node [class="planning", reasoning_effort=medium]
		a
		b [reasoning_effort=high]
		done [shape=Msquare]
		start -> a -> b -> done
	}`)
	if got := g.Nodes["a"].Attrs["class"]; got != "planning" {
		t.Fatalf("a.class=%q", got)
	}
	if got := g.Nodes["a"].Attrs["reasoning_effort"]; got != "medium" {
		t.Fatalf("a.reasoning_effort=%q", got)
	}
	if got := g.Nodes["b"].Attrs["reasoning_effort"]; got != "high" {
		t.Fatalf("b.reasoning_effort=%q", got)
	}
}

func TestBuild_EdgeDefaultsApplied(t *testing.T) {
	g := buildGraph(t, `digraph g {
		start [shape=Mdiamond]
		done [shape=Msquare]
		edge [weight=5]
		start -> done [label="go"]
	}`)
	e := g.OutgoingEdges("start")[0]
	if e.Weight() != 5 {
		t.Fatalf("edge weight=%d, want 5", e.Weight())
	}
	if e.Label() != "go" {
		t.Fatalf("edge label=%q", e.Label())
	}
}

func TestBuild_SubgraphScopeFlatten(t *testing.T) {
	g := buildGraph(t, `digraph g {
		start [shape=Mdiamond]
		done [shape=Msquare]
		subgraph cluster_critique {
			node [type="codergen.openai"]
			r1 [prompt="critique"]
			r2 [prompt="critique 2"]
		}
		start -> r1 -> r2 -> done
	}`)
	if got := g.Nodes["r1"].Attrs["type"]; got != "codergen.openai" {
		t.Fatalf("r1.type=%q", got)
	}
	if got := len(g.Nodes["r1"].Subgraphs); got != 1 || g.Nodes["r1"].Subgraphs[0] != "cluster_critique" {
		t.Fatalf("r1.subgraphs=%v", g.Nodes["r1"].Subgraphs)
	}
	// Subgraph defaults must not leak to outer-scope nodes.
	if _, ok := g.Nodes["start"].Attrs["type"]; ok {
		t.Fatalf("start should not inherit subgraph default")
	}
}

func TestBuild_ImplicitNodeFromEdge(t *testing.T) {
	g := buildGraph(t, `digraph g {
		start [shape=Mdiamond]
		done [shape=Msquare]
		start -> implicit -> done
	}`)
	n, ok := g.Nodes["implicit"]
	if !ok {
		t.Fatal("implicit node should be created from edge declaration")
	}
	if n.Shape() != "box" {
		t.Fatalf("implicit.shape=%q, want default box", n.Shape())
	}
}

func TestNodeShapeHandlerMapping(t *testing.T) {
	g := buildGraph(t, `digraph g {
		start [shape=Mdiamond]
		gate  [shape=hexagon]
		fan   [shape=component]
		join  [shape=tripleoctagon]
		t1    [shape=parallelogram]
		boss  [shape=house]
		done  [shape=Msquare]
		start -> gate -> fan -> join -> t1 -> boss -> done
	}`)
	cases := []struct{ id, want string }{
		{"start", "start"},
		{"gate", "wait.human"},
		{"fan", "parallel"},
		{"join", "parallel.fan_in"},
		{"t1", "tool"},
		{"boss", "stack.manager_loop"},
		{"done", "exit"},
	}
	for _, c := range cases {
		if got := g.Nodes[c.id].Type(); got != c.want {
			t.Fatalf("%s.Type()=%q, want %q", c.id, got, c.want)
		}
	}
}

func TestNodeTypeOverridesShape(t *testing.T) {
	g := buildGraph(t, `digraph g {
		start [shape=Mdiamond]
		x [shape=box, type="codergen.claude.tmux"]
		done [shape=Msquare]
		start -> x -> done
	}`)
	if got := g.Nodes["x"].Type(); got != "codergen.claude.tmux" {
		t.Fatalf("explicit type override failed: %q", got)
	}
}

func TestNodeDurationParsing(t *testing.T) {
	g := buildGraph(t, `digraph g {
		start [shape=Mdiamond]
		a [timeout=20m]
		done [shape=Msquare]
		start -> a -> done
	}`)
	d, ok := g.Nodes["a"].Duration("timeout")
	if !ok || d != 20*time.Minute {
		t.Fatalf("Duration(timeout)=%v ok=%v", d, ok)
	}
}

func TestVariableExpansionTransform(t *testing.T) {
	g := buildGraph(t, `digraph g {
		goal = "Add a sort function"
		start [shape=Mdiamond]
		plan [prompt="Plan: $goal"]
		impl [prompt="Implement: $goal, all of it"]
		done [shape=Msquare]
		start -> plan -> impl -> done
	}`)
	g, err := transform.Apply(g, []transform.Transform{transform.VariableExpansion{}})
	must(t, err)
	if got := g.Nodes["plan"].Prompt(); got != "Plan: Add a sort function" {
		t.Fatalf("plan prompt=%q", got)
	}
	if got := g.Nodes["impl"].Prompt(); got != "Implement: Add a sort function, all of it" {
		t.Fatalf("impl prompt=%q", got)
	}
}

func TestStylesheetSpecificity(t *testing.T) {
	g := buildGraph(t, `digraph g {
		model_stylesheet = "
			* { llm_model: claude-sonnet-4-5; llm_provider: anthropic; }
			.code { llm_model: claude-opus-4-6; }
			#critical_review { llm_model: gpt-5.2; llm_provider: openai; reasoning_effort: high; }
		"
		start [shape=Mdiamond]
		plan [class="planning"]
		implement [class="code"]
		critical_review [class="code"]
		done [shape=Msquare]
		start -> plan -> implement -> critical_review -> done
	}`)
	g, err := transform.Apply(g, []transform.Transform{transform.Stylesheet{}})
	must(t, err)
	checks := []struct {
		id           string
		llm_model    string
		llm_provider string
	}{
		{"plan", "claude-sonnet-4-5", "anthropic"},
		{"implement", "claude-opus-4-6", "anthropic"},
		{"critical_review", "gpt-5.2", "openai"},
	}
	for _, c := range checks {
		if got := g.Nodes[c.id].Attrs["llm_model"]; got != c.llm_model {
			t.Fatalf("%s.llm_model=%q, want %q", c.id, got, c.llm_model)
		}
		if got := g.Nodes[c.id].Attrs["llm_provider"]; got != c.llm_provider {
			t.Fatalf("%s.llm_provider=%q, want %q", c.id, got, c.llm_provider)
		}
	}
}

func TestStylesheetExplicitOverridesStylesheet(t *testing.T) {
	g := buildGraph(t, `digraph g {
		model_stylesheet = "* { llm_model: claude-sonnet-4-5; }"
		start [shape=Mdiamond]
		x [llm_model="gpt-5.2"]
		done [shape=Msquare]
		start -> x -> done
	}`)
	g, err := transform.Apply(g, []transform.Transform{transform.Stylesheet{}})
	must(t, err)
	if got := g.Nodes["x"].Attrs["llm_model"]; got != "gpt-5.2" {
		t.Fatalf("explicit attr should win: %q", got)
	}
}
