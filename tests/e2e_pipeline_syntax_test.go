package attractor_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/cli"
)

// shippedPipelineFiles are the dotfiles and prompt files that interpolate
// runtime context. After the C5 migration every variable reference uses
// `$context.<key>` (or the `$goal` built-in); no bare `$var` remains for
// the prepare-time transform to expand. This guards the migration and,
// once that transform is removed (C7), pins that nothing regresses to
// bare-var syntax. Shell forms (`$(…)`, `$$`) are left for the shell.
var shippedPipelineFiles = []string{
	"../pipelines/review/pipeline.dot",
	"../pipelines/review/prompts/review.md",
	"../pipelines/implement/pipeline.dot",
	"../pipelines/implement/prompts/implement.md",
	"../pipelines/build/pipeline.dot",
	"../pipelines/build/prompts/plan.md",
	"../pipelines/build/prompts/implement.md",
	"../pipelines/build/prompts/review.md",
	"../pipelines/build/prompts/record.md",
	"../pipelines/router/pipeline.dot",
	"../pipelines/router/prompts/triage.md",
}

// bareVarRe matches a `$` that begins an identifier — a bare variable
// reference. Sanctioned forms (`$context.`, `$goal`, `$$`) are stripped
// before matching; shell `$(…)` never starts with a letter, so it is
// left alone.
var bareVarRe = regexp.MustCompile(`\$[A-Za-z_]`)

func TestShippedPipelines_UseContextSyntax(t *testing.T) {
	for _, f := range shippedPipelineFiles {
		b, err := os.ReadFile(f)
		must(t, err)
		s := string(b)
		s = strings.ReplaceAll(s, "$context.", "")
		s = strings.ReplaceAll(s, "$goal", "")
		s = strings.ReplaceAll(s, "$$", "")
		if m := bareVarRe.FindString(s); m != "" {
			t.Errorf("%s: bare variable reference %q — migrate to $context. syntax", f, m)
		}
	}
}

// TestBareVar_NoLongerExpands pins the C7 removal: with the prepare-time
// VariableExpansion transform gone, a bare `$epic_id` is left verbatim
// (the shell owns `$`), while the seeded `$context.epic_id` still resolves
// at runtime. Both names are seeded by the same `-var epic_id=ABC-123`.
func TestBareVar_NoLongerExpands(t *testing.T) {
	tmp := t.TempDir()
	must(t, os.WriteFile(filepath.Join(tmp, "pipeline.dot"), []byte(`digraph demo {
		vars = "epic_id"
		start [shape=Mdiamond]
		work [prompt="legacy $epic_id vs $context.epic_id"]
		done [shape=Msquare]
		start -> work -> done
	}`), 0o644))

	logsRoot := t.TempDir()
	err := cli.Run([]string{
		"--backend", "simulation",
		"--logs", logsRoot,
		"--var", "epic_id=ABC-123",
		filepath.Join(tmp, "pipeline.dot"),
	})
	must(t, err)
	body, err := os.ReadFile(filepath.Join(logsRoot, "work", "prompt.md"))
	must(t, err)
	if got := string(body); !strings.Contains(got, "legacy $epic_id vs ABC-123") {
		t.Fatalf("bare $var should stay literal, $context. should resolve: %q", got)
	}
}
