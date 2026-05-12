package transform

import "github.com/fabro/attractor/internal/graph"

// Transform is the AST-transform interface (spec §9.1). Implementations
// receive a Graph and return a (possibly new) Graph.
type Transform interface {
	Apply(g *graph.Graph) (*graph.Graph, error)
}

// BuiltIn returns the parse-time transforms applied to every pipeline.
// Order matches spec §9.2: stylesheet first so explicit overrides
// remain authoritative, then variable expansion so $goal references can
// land in synthesised attributes.
func BuiltIn() []Transform {
	return []Transform{
		Stylesheet{},
		VariableExpansion{},
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
