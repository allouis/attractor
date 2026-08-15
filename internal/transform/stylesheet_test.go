package transform

import (
	"testing"

	"github.com/allouis/attractor/internal/graph"
)

// applySheet builds a graph from src and overlays the external stylesheet.
func applySheet(t *testing.T, src, sheet string) *graph.Graph {
	t.Helper()
	g := buildG(t, src)
	out, err := (Stylesheet{Source: sheet}).Apply(g)
	if err != nil {
		t.Fatalf("stylesheet apply: %v", err)
	}
	return out
}

func TestStylesheet_ExternalSourceOverlaysUnpinnedNodes(t *testing.T) {
	src := `digraph g {
		start [shape=Mdiamond]
		plan   [class="plan", prompt="p"]
		impl   [class="build", prompt="i"]
		done   [shape=Msquare]
		start -> plan -> impl -> done
	}`
	sheet := `
		* { llm_model: claude-fable-5 }
		.build { llm_model: claude-opus-4-8 }
	`
	g := applySheet(t, src, sheet)
	if got := g.Nodes["plan"].Attrs["llm_model"]; got != "claude-fable-5" {
		t.Errorf("plan model = %q, want claude-fable-5 (from *)", got)
	}
	if got := g.Nodes["impl"].Attrs["llm_model"]; got != "claude-opus-4-8" {
		t.Errorf("impl model = %q, want claude-opus-4-8 (.build beats *)", got)
	}
}

func TestStylesheet_IDBeatsClassBeatsUniversal(t *testing.T) {
	src := `digraph g {
		impl [class="build", prompt="i"]
	}`
	sheet := `
		* { llm_model: m-universal }
		.build { llm_model: m-class }
		#impl { llm_model: m-id }
	`
	g := applySheet(t, src, sheet)
	if got := g.Nodes["impl"].Attrs["llm_model"]; got != "m-id" {
		t.Errorf("impl model = %q, want m-id (#id highest specificity)", got)
	}
}

func TestStylesheet_ExplicitNodeAttrWins(t *testing.T) {
	// A mandatory pin (e.g. the codex review lens) must survive the sheet.
	src := `digraph g {
		correctness [class="review", llm_provider="codex", llm_model="gpt-5.6-sol", prompt="c"]
	}`
	sheet := `.review { llm_model: claude-opus-4-8 }`
	g := applySheet(t, src, sheet)
	if got := g.Nodes["correctness"].Attrs["llm_model"]; got != "gpt-5.6-sol" {
		t.Errorf("correctness model = %q, want gpt-5.6-sol (explicit pin wins)", got)
	}
}

func TestStylesheet_EmptySourceIsNoop(t *testing.T) {
	src := `digraph g { impl [class="build", prompt="i"] }`
	g := applySheet(t, src, "")
	if got := g.Nodes["impl"].Attrs["llm_model"]; got != "" {
		t.Errorf("impl model = %q, want empty (no sheet)", got)
	}
}
