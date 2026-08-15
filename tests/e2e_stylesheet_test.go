package attractor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/setup"
)

// TestStylesheet_AppliesToImplementIncludingInlinedReview proves the
// external --stylesheet overlay reaches role classes AND inlined subgraph
// nodes, while an explicit node pin (the codex lens) survives.
func TestStylesheet_AppliesToImplementIncludingInlinedReview(t *testing.T) {
	dir, err := filepath.Abs("../pipelines/implement")
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

	want := map[string]string{
		"plan":                    "claude-fable-5[1m]",  // .plan
		"implement":               "claude-opus-4-8[1m]", // .build
		"fix_checks":              "claude-opus-4-8[1m]", // .build
		"open_pr":                 "claude-fable-5[1m]",  // .publish
		"review_loop.design":      "claude-opus-4-8[1m]", // inlined .review
		"review_loop.synth":       "claude-opus-4-8[1m]", // inlined .review
		"review_loop.correctness": "gpt-5.6-sol[high]",   // explicit codex pin wins
	}
	for id, model := range want {
		n := g.Nodes[id]
		if n == nil {
			t.Fatalf("node %q missing from prepared graph", id)
		}
		if got := n.Attrs["llm_model"]; got != model {
			t.Errorf("node %q llm_model = %q, want %q", id, got, model)
		}
	}
}
