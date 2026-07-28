package transform

import "github.com/allouis/attractor/internal/graph"

// Transform is the AST-transform interface (spec §9.1). Implementations
// receive a Graph and return a (possibly new) Graph.
type Transform interface {
	Apply(g *graph.Graph) (*graph.Graph, error)
}

// BuiltIn returns the parse-time transforms applied to every pipeline.
// Order matches spec §9.2: stylesheet first so explicit overrides remain
// authoritative. Variable substitution is no longer a transform — nodes
// interpolate `$context.*`/`$goal` from the live context at runtime
// (spec §4.5), so there is nothing to expand at parse time.
func BuiltIn() []Transform {
	return []Transform{
		Stylesheet{},
	}
}

// Apply runs the supplied transforms in order, threading the graph
// through each. The first error short-circuits the pipeline.
func Apply(g *graph.Graph, transforms []Transform) (*graph.Graph, error) {
	for _, t := range transforms {
		next, err := t.Apply(g)
		if err != nil {
			return nil, err
		}
		g = next
	}
	return g, nil
}
