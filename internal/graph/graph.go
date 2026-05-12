// Package graph defines the Attractor pipeline graph model used by all
// engine and handler code. A Graph is built from a parsed dot.File by the
// Build function; subgraphs are flattened, defaults blocks are merged
// into their successor declarations, and chained edges are expanded.
package graph

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fabro/attractor/internal/dot"
)

// Graph is the canonical pipeline model. Attribute lookups go through the
// typed accessors below; raw string values live in Attrs/Node.Attrs/Edge.Attrs
// to keep parser and engine concerns separate.
type Graph struct {
	Name           string
	Attrs          map[string]string
	Nodes          map[string]*Node
	NodeOrder      []string
	Edges          []*Edge
	edgesByFromIdx map[string][]int
}

// Node is a pipeline stage in the graph. Subgraphs records the names of
// any subgraphs the node was declared inside, in nesting order. The
// stylesheet transform uses Subgraphs to resolve scope-class membership.
type Node struct {
	ID        string
	Attrs     map[string]string
	Subgraphs []string
}

// Edge is a directed link between two nodes.
type Edge struct {
	From  string
	To    string
	Attrs map[string]string
}

// Build constructs a Graph from a parsed dot.File. It flattens subgraphs,
// applies defaults blocks to the declarations that follow them (within
// the same scope), and expands chained edges into pairwise edges. The
// returned Graph is order-preserving over node declarations.
func Build(file *dot.File) (*Graph, error) {
	g := &Graph{
		Name:           file.Name,
		Attrs:          map[string]string{},
		Nodes:          map[string]*Node{},
		edgesByFromIdx: map[string][]int{},
	}
	walker := &builder{graph: g}
	if err := walker.walk(file.Statements, nil, nil, nil); err != nil {
		return nil, err
	}
	return g, nil
}

type builder struct {
	graph *Graph
}

func (b *builder) walk(stmts []dot.Statement, nodeDefaults, edgeDefaults map[string]string, subgraphStack []string) error {
	if nodeDefaults == nil {
		nodeDefaults = map[string]string{}
	}
	if edgeDefaults == nil {
		edgeDefaults = map[string]string{}
	}
	for _, stmt := range stmts {
		switch s := stmt.(type) {
		case dot.DeclStmt:
			b.graph.Attrs[s.Key] = s.Value
		case dot.GraphAttrStmt:
			for k, v := range s.Attrs {
				b.graph.Attrs[k] = v
			}
		case dot.NodeDefaults:
			nodeDefaults = mergeAttrs(nodeDefaults, s.Attrs)
		case dot.EdgeDefaults:
			edgeDefaults = mergeAttrs(edgeDefaults, s.Attrs)
		case dot.SubgraphStmt:
			stack := append(append([]string{}, subgraphStack...), s.Name)
			// Subgraph scope: copy defaults so changes inside don't leak.
			ndCopy := cloneAttrs(nodeDefaults)
			edCopy := cloneAttrs(edgeDefaults)
			if err := b.walk(s.Statements, ndCopy, edCopy, stack); err != nil {
				return err
			}
		case dot.NodeStmt:
			b.addNode(s, nodeDefaults, subgraphStack)
		default:
			if dot.IsEdge(stmt) {
				for _, e := range dot.EdgesOf(stmt) {
					b.addEdge(e, edgeDefaults)
				}
			} else {
				return fmt.Errorf("graph build: unknown statement type %T", stmt)
			}
		}
	}
	return nil
}

func (b *builder) addNode(s dot.NodeStmt, defaults map[string]string, subgraphStack []string) {
	existing, ok := b.graph.Nodes[s.ID]
	if !ok {
		n := &Node{
			ID:        s.ID,
			Attrs:     mergeAttrs(defaults, s.Attrs),
			Subgraphs: append([]string{}, subgraphStack...),
		}
		b.graph.Nodes[s.ID] = n
		b.graph.NodeOrder = append(b.graph.NodeOrder, s.ID)
		return
	}
	// A second declaration merges its attributes onto the existing node,
	// matching graphviz semantics where node attributes accumulate. The
	// existing subgraph trail is preserved.
	existing.Attrs = mergeAttrs(existing.Attrs, mergeAttrs(defaults, s.Attrs))
}

func (b *builder) addEdge(s dot.EdgeStmt, defaults map[string]string) {
	e := &Edge{From: s.From, To: s.To, Attrs: mergeAttrs(defaults, s.Attrs)}
	b.graph.Edges = append(b.graph.Edges, e)
	b.graph.edgesByFromIdx[e.From] = append(b.graph.edgesByFromIdx[e.From], len(b.graph.Edges)-1)
	// Ensure implicit nodes referenced by edges exist with empty attrs so
	// the lint phase can report them by ID without surprises.
	for _, id := range []string{s.From, s.To} {
		if _, ok := b.graph.Nodes[id]; !ok {
			b.graph.Nodes[id] = &Node{ID: id, Attrs: map[string]string{}}
			b.graph.NodeOrder = append(b.graph.NodeOrder, id)
		}
	}
}

// OutgoingEdges returns edges originating from the given node in
// declaration order.
func (g *Graph) OutgoingEdges(id string) []*Edge {
	idx := g.edgesByFromIdx[id]
	out := make([]*Edge, 0, len(idx))
	for _, i := range idx {
		out = append(out, g.Edges[i])
	}
	return out
}

// IncomingEdges returns edges terminating at the given node.
func (g *Graph) IncomingEdges(id string) []*Edge {
	var out []*Edge
	for _, e := range g.Edges {
		if e.To == id {
			out = append(out, e)
		}
	}
	return out
}

// Goal returns the graph-level goal attribute (mirrored into the run
// context as `graph.goal`).
func (g *Graph) Goal() string { return g.Attrs["goal"] }

// Attr returns a graph-level attribute by key with empty-string default.
func (g *Graph) Attr(key string) string { return g.Attrs[key] }

// IntAttr returns a graph-level integer attribute. Missing or malformed
// values return the supplied default.
func (g *Graph) IntAttr(key string, def int) int {
	v, ok := g.Attrs[key]
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// ---------------------------------------------------------------------------
// Node accessors
// ---------------------------------------------------------------------------

// Shape returns the node's `shape` attribute, defaulting to `box`.
func (n *Node) Shape() string {
	if s, ok := n.Attrs["shape"]; ok && s != "" {
		return s
	}
	return "box"
}

// Type returns the handler type. The explicit `type` attribute wins;
// otherwise shape-based resolution is used.
func (n *Node) Type() string {
	if t, ok := n.Attrs["type"]; ok && t != "" {
		return t
	}
	return TypeFromShape(n.Shape())
}

// Label returns the node label, falling back to the node ID.
func (n *Node) Label() string {
	if v, ok := n.Attrs["label"]; ok && v != "" {
		return v
	}
	return n.ID
}

// Prompt returns the node's prompt attribute (empty if unset).
func (n *Node) Prompt() string { return n.Attrs["prompt"] }

// Bool returns a boolean attribute. The "true" / "false" literals match
// the DOT subset's Boolean value type.
func (n *Node) Bool(key string) bool { return n.Attrs[key] == "true" }

// Int returns an integer attribute with a default fallback.
func (n *Node) Int(key string, def int) int {
	v, ok := n.Attrs[key]
	if !ok {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}

// Duration returns a time.Duration parsed from the attribute. Supports
// the spec's `ms / s / m / h / d` suffixes. Missing or malformed values
// return zero with ok=false.
func (n *Node) Duration(key string) (time.Duration, bool) {
	v, ok := n.Attrs[key]
	if !ok || v == "" {
		return 0, false
	}
	return ParseDuration(v)
}

// Classes returns the node's class list (split on comma from the
// `class` attribute).
func (n *Node) Classes() []string {
	v := strings.TrimSpace(n.Attrs["class"])
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Edge accessors
// ---------------------------------------------------------------------------

// Attr returns an edge attribute by key (empty if unset).
func (e *Edge) Attr(key string) string { return e.Attrs[key] }

// Bool returns a boolean edge attribute.
func (e *Edge) Bool(key string) bool { return e.Attrs[key] == "true" }

// Weight returns the edge `weight` attribute as an integer.
func (e *Edge) Weight() int {
	v, ok := e.Attrs["weight"]
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// Label returns the edge label (empty if unset).
func (e *Edge) Label() string { return e.Attrs["label"] }

// Condition returns the edge condition expression (empty if unset).
func (e *Edge) Condition() string { return e.Attrs["condition"] }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// TypeFromShape resolves a Graphviz shape to its canonical handler type
// per Attractor Appendix B. Unrecognised shapes default to "codergen".
func TypeFromShape(shape string) string {
	switch shape {
	case "Mdiamond":
		return "start"
	case "Msquare":
		return "exit"
	case "box":
		return "codergen"
	case "hexagon":
		return "wait.human"
	case "diamond":
		return "conditional"
	case "component":
		return "parallel"
	case "tripleoctagon":
		return "parallel.fan_in"
	case "parallelogram":
		return "tool"
	case "house":
		return "stack.manager_loop"
	}
	return "codergen"
}

// ParseDuration parses a duration value using the DOT subset's allowed
// suffixes: ms, s, m, h, d.
func ParseDuration(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	suffixes := []struct {
		s string
		u time.Duration
	}{
		{"ms", time.Millisecond},
		{"s", time.Second},
		{"m", time.Minute},
		{"h", time.Hour},
		{"d", 24 * time.Hour},
	}
	for _, suf := range suffixes {
		if strings.HasSuffix(v, suf.s) {
			head := strings.TrimSuffix(v, suf.s)
			n, err := strconv.ParseFloat(head, 64)
			if err != nil {
				continue
			}
			return time.Duration(n * float64(suf.u)), true
		}
	}
	// Fall back to Go's ParseDuration for nanosecond-level inputs.
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, false
	}
	return d, true
}

// mergeAttrs returns a new map containing all keys from base overlaid by
// overrides. The base map is never mutated.
func mergeAttrs(base, overrides map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(overrides))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

func cloneAttrs(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
