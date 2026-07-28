package attractor_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
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
