package attractor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/setup"
)

// TestStylesheet_AppliesToImplementIncludingInlinedReview proves the
// external --stylesheet overlay reaches role classes AND inlined subgraph
// nodes, while the correctness class selects Codex over the Claude defaults.
func TestStylesheet_AppliesToImplementIncludingInlinedReview(t *testing.T) {
	dir, err := filepath.Abs("../pipelines/plan-build-review")
	must(t, err)
	src, err := os.ReadFile(filepath.Join(dir, "pipeline.dot"))
	must(t, err)
	sheet, err := os.ReadFile("../pipelines/models.css")
	must(t, err)

	pg, err := setup.Prepare(setup.Options{
		Source:     string(src),
		BaseDir:    dir,
		Stylesheet: string(sheet),
	})
	must(t, err)
	g := pg.Graph

	want := map[string]struct{ model, provider string }{
		"plan":                    {"fable", "anthropic"},         // .plan
		"implement":               {"opus", "anthropic"},          // .build
		"fix_checks":              {"opus", "anthropic"},          // .build
		"review_loop.design":      {"opus", "anthropic"},          // inlined .review
		"review_loop.synth":       {"opus", "anthropic"},          // explicit stylesheet ID
		"review_loop.correctness": {"gpt-6.1-sol[high]", "codex"}, // .correctness overrides .review
	}
	for id, expected := range want {
		n := g.Nodes[id]
		if n == nil {
			t.Fatalf("node %q missing from prepared graph", id)
		}
		if got := n.Attrs["llm_model"]; got != expected.model {
			t.Errorf("node %q llm_model = %q, want %q", id, got, expected.model)
		}
		if got := n.Attrs["llm_provider"]; got != expected.provider {
			t.Errorf("node %q llm_provider = %q, want %q", id, got, expected.provider)
		}
	}
}
