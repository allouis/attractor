// Package setup is the one shared pipeline preparation path used by both
// entry points — the CLI `run` command and the HTTP `serve` daemon
// (service-spec §2). It parses DOT source, validates supplied vars,
// applies the standard transforms (@file prompt inlining, $var
// expansion), defaults the graph-level cwd, and lints, returning a
// PreparedGraph ready for engine.Run. Keeping this in one place is what
// gives run/serve parity: both accept vars, @file prompts, and a cwd.
package setup

import (
	"fmt"
	"strings"

	"github.com/fabro/attractor/internal/dot"
	"github.com/fabro/attractor/internal/engine"
	"github.com/fabro/attractor/internal/graph"
	"github.com/fabro/attractor/internal/transform"
)

// Options are the inputs to the shared setup path.
type Options struct {
	// Source is the raw DOT text of the pipeline.
	Source string
	// Vars maps $placeholder names (without the leading $) to expansion
	// values, supplied by CLI -var flags or the serve submit payload.
	Vars map[string]string
	// BaseDir resolves @file prompt references. The CLI passes the .dot
	// file's directory; serve passes the submission cwd.
	BaseDir string
	// Cwd becomes the graph-level cwd default (service-spec §2): it is
	// applied only when the graph declares no cwd, so node/graph attrs
	// still win. Empty leaves the graph untouched.
	Cwd string
}

// Prepare parses, validates, transforms, and lints the pipeline source
// into a PreparedGraph. Errors cover parse/build failures, missing
// declared vars, transform failures (e.g. a missing @file), and lint
// errors.
func Prepare(o Options) (*engine.PreparedGraph, error) {
	file, err := dot.Parse(o.Source)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	g, err := graph.Build(file)
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	if err := RequireDeclaredVars(g, o.Vars); err != nil {
		return nil, err
	}
	if o.Cwd != "" && g.Attrs["cwd"] == "" {
		g.Attrs["cwd"] = o.Cwd
	}
	return engine.Prepare(g,
		transform.PromptFile{BaseDir: o.BaseDir},
		transform.VariableExpansion{Vars: o.Vars},
	)
}

// RequireDeclaredVars checks that every name listed in the graph's
// `vars` attribute has a corresponding entry in supplied. Missing vars
// are a hard error so a pipeline doesn't silently run with empty
// substitutions.
func RequireDeclaredVars(g *graph.Graph, supplied map[string]string) error {
	raw := strings.TrimSpace(g.Attr("vars"))
	if raw == "" {
		return nil
	}
	var missing []string
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := supplied[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("pipeline declares vars %v but missing values for %v", strings.Split(raw, ","), missing)
}
