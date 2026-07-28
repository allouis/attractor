package attractor_test

import (
	"os"
	"testing"

	"github.com/allouis/attractor/internal/lint"
)

// TestImplementPipeline_LintsClean confirms the shipped implement
// pipeline — the router's `issue` target (router-spec R6) — is
// structurally valid: it parses, builds, and lints with no ERROR.
func TestImplementPipeline_LintsClean(t *testing.T) {
	b, err := os.ReadFile("../pipelines/implement/pipeline.dot")
	must(t, err)
	g := build(t, string(b))
	diags, err := lint.ValidateOrError(g)
	if err != nil {
		t.Fatalf("implement pipeline rejected: %v", err)
	}
	for _, d := range diags {
		if d.Severity == lint.Error {
			t.Fatalf("unexpected ERROR: %+v", d)
		}
	}
}
