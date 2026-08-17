package render

import (
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/graph"
)

func buildGraph(t *testing.T, src string) *graph.Graph {
	t.Helper()
	file, err := dot.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g, err := graph.Build(file)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return g
}

const metaGraph = `digraph d {
	start [shape=Mdiamond]
	build [type="codergen.acp", llm_model="claude-opus", thread_id="t1"]
	start -> build
}`

// TopologyDOT persists the explicit type and the codergen routing
// attributes so a re-served run reconstructs them; MinimalDOT stays clean
// for graphviz.
func TestTopologyDOTCarriesMetadataMinimalDoesNot(t *testing.T) {
	g := buildGraph(t, metaGraph)

	topo := string(TopologyDOT(g))
	for _, want := range []string{`type="codergen.acp"`, `llm_model="claude-opus"`, `thread_id="t1"`} {
		if !strings.Contains(topo, want) {
			t.Errorf("TopologyDOT missing %q:\n%s", want, topo)
		}
	}

	minimal := string(MinimalDOT(g))
	for _, absent := range []string{"llm_model", "thread_id", "codergen.acp"} {
		if strings.Contains(minimal, absent) {
			t.Errorf("MinimalDOT should stay clean but contains %q:\n%s", absent, minimal)
		}
	}
}

// TopologyDOT must be re-parseable by the Attractor dot parser (bare ids,
// no graphviz header) — the property viewMeta depends on — and preserve
// the explicit handler type (which shape=box cannot carry) plus the
// routing attributes.
func TestTopologyDOTRoundTrips(t *testing.T) {
	g := buildGraph(t, metaGraph)
	g2 := buildGraph(t, string(TopologyDOT(g))) // fails the test if unparseable
	if got := g2.Nodes["build"].Attrs["llm_model"]; got != "claude-opus" {
		t.Fatalf("round-tripped llm_model = %q, want claude-opus", got)
	}
	if got := g2.Nodes["build"].Type(); got != "codergen.acp" {
		t.Fatalf("round-tripped type = %q, want codergen.acp", got)
	}
	if got := g2.Nodes["start"].Type(); got != "start" {
		t.Fatalf("round-tripped start type = %q, want start", got)
	}
}
