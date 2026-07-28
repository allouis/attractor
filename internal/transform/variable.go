// Package transform implements Attractor's AST transforms (spec §9):
// modifications applied to a Graph between parsing and validation.
package transform

import (
	"strings"

	"github.com/allouis/attractor/internal/graph"
)

// VariableExpansion replaces `$ident` placeholders in each node's
// prompt attribute with values from the supplied map. The map is
// merged with `goal`, so $goal continues to resolve to the graph's
// goal attribute (spec §4.5). Additional names come from CLI args or
// any other caller-supplied source.
type VariableExpansion struct {
	// Vars maps placeholder names (without the leading $) to expansion
	// values. Empty when only $goal expansion is desired.
	Vars map[string]string
}

// Apply rewrites any attribute containing a `$` placeholder in the
// graph, node, or edge namespaces. The `goal` graph attribute is
// expanded first so subsequent `$goal` references in prompts and other
// attributes pick up the already-substituted value.
func (v VariableExpansion) Apply(g *graph.Graph) (*graph.Graph, error) {
	values := map[string]string{}
	for k, val := range v.Vars {
		values[k] = val
	}
	if g.Attrs["goal"] != "" && strings.Contains(g.Attrs["goal"], "$") {
		g.Attrs["goal"] = expandVars(g.Attrs["goal"], values)
	}
	if _, ok := values["goal"]; !ok {
		values["goal"] = g.Goal()
	}
	for k, val := range g.Attrs {
		if strings.Contains(val, "$") {
			g.Attrs[k] = expandVars(val, values)
		}
	}
	for _, id := range g.NodeOrder {
		node := g.Nodes[id]
		for k, val := range node.Attrs {
			if strings.Contains(val, "$") {
				node.Attrs[k] = expandVars(val, values)
			}
		}
	}
	for _, e := range g.Edges {
		for k, val := range e.Attrs {
			if strings.Contains(val, "$") {
				e.Attrs[k] = expandVars(val, values)
			}
		}
	}
	return g, nil
}

// expandVars walks the input replacing `$ident` runs with the value
// from values, leaving unknown names verbatim. Identifiers follow the
// usual `[A-Za-z_][A-Za-z0-9_]*` shape. `$$` is treated as a literal
// `$` for callers that need to emit the character itself.
func expandVars(s string, values map[string]string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' {
			b.WriteByte('$')
			i += 2
			continue
		}
		// Scan an identifier run.
		j := i + 1
		for j < len(s) && isIdentByte(s[j]) {
			j++
		}
		if j == i+1 {
			// Lone `$` with no identifier — leave it alone.
			b.WriteByte('$')
			i++
			continue
		}
		name := s[i+1 : j]
		if val, ok := values[name]; ok {
			b.WriteString(val)
		} else {
			b.WriteString(s[i:j])
		}
		i = j
	}
	return b.String()
}

func isIdentByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z':
		return true
	case c >= 'A' && c <= 'Z':
		return true
	case c >= '0' && c <= '9':
		return true
	case c == '_':
		return true
	}
	return false
}
