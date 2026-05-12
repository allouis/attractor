// Package transform implements Attractor's AST transforms (spec §9):
// modifications applied to a Graph between parsing and validation.
package transform

import (
	"strings"

	"github.com/fabro/attractor/internal/graph"
)

// VariableExpansion replaces `$goal` placeholders in each node's prompt
// attribute with the graph-level `goal` attribute. Variable expansion is
// simple string replacement, not a templating engine (spec §4.5).
type VariableExpansion struct{}

// Apply rewrites prompts in place and returns the same graph.
func (VariableExpansion) Apply(g *graph.Graph) (*graph.Graph, error) {
	goal := g.Goal()
	if goal == "" {
		return g, nil
	}
	for _, id := range g.NodeOrder {
		node := g.Nodes[id]
		prompt, ok := node.Attrs["prompt"]
		if !ok || !strings.Contains(prompt, "$goal") {
			continue
		}
		node.Attrs["prompt"] = strings.ReplaceAll(prompt, "$goal", goal)
	}
	return g, nil
}
