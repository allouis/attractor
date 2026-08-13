// Package setup is the one shared pipeline preparation path used by both
// entry points — the CLI `run` command and the HTTP `serve` daemon
// (service-spec §2). It parses DOT source, applies the standard transforms
// (@file prompt inlining, stylesheet), defaults the graph-level cwd, and
// lints, returning a PreparedGraph ready for engine.Run. Keeping this in
// one place is what gives run/serve parity: both accept @file prompts and
// a cwd. Vars are seeded into the run context by the caller (C3), not here;
// `$context.*` interpolates at runtime (spec §4.5).
package setup

import (
	"fmt"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/transform"
)

// Options are the inputs to the shared setup path.
type Options struct {
	// Source is the raw DOT text of the pipeline.
	Source string
	// BaseDir resolves @file prompt references. The CLI passes the .dot
	// file's directory; serve passes the submission cwd.
	BaseDir string
	// Cwd becomes the graph-level cwd default (service-spec §2): it is
	// applied only when the graph declares no cwd, so node/graph attrs
	// still win. Empty leaves the graph untouched.
	Cwd string
}

// Prepare parses, transforms, and lints the pipeline source into a
// PreparedGraph. Errors cover parse/build failures, transform failures
// (e.g. a missing @file), and lint errors. The `vars=` input contract is
// validated at run-start against the seeded context by the engine, not
// here (spec §"Locked decisions" 6), so every run path validates once.
func Prepare(o Options) (*engine.PreparedGraph, error) {
	file, err := dot.Parse(o.Source)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	g, err := graph.Build(file)
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	if o.Cwd != "" && g.Attrs["cwd"] == "" {
		g.Attrs["cwd"] = o.Cwd
	}
	pg, err := engine.Prepare(g,
		transform.Subgraph{BaseDir: o.BaseDir},
		transform.PromptFile{BaseDir: o.BaseDir},
	)
	if err != nil {
		return nil, err
	}
	// Record where the pipeline was loaded from so runtime handlers can
	// resolve relative references (e.g. a subgraph graph_ref)
	// independent of the process cwd. Set after Prepare so transforms,
	// which may rebuild the graph, cannot drop it.
	pg.Graph.BaseDir = o.BaseDir
	return pg, nil
}
