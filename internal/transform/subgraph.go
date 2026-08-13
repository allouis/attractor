package transform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/graph"
)

// Subgraph statically expands `type="subgraph"` nodes (local-first plan
// D6, spec §9.4's static-composition half): the referenced child
// pipeline is inlined at load time, node IDs prefixed with the subgraph
// node's ID, the child's start/exit spliced into the node's own edges.
// Children must be known at parse time; there is no runtime dispatch
// node.
//
//	review [type="subgraph", graph_ref="../review-core/pipeline.dot",
//	        var.diff_cmd="jj diff"]
//
// `var.<name>` attrs satisfy the child's declared vars by static
// substitution: every `$context.<name>` in the child's attrs is
// replaced with the value, which may itself carry `$context.*`
// references that resolve at runtime against the (shared) parent
// context. The child's own @file prompts resolve relative to the
// child's directory before substitution.
type Subgraph struct {
	// BaseDir resolves a relative graph_ref (the parent pipeline's dir).
	BaseDir string
	// expanding tracks graph_ref paths on the current expansion stack to
	// fail cyclic references instead of recursing forever.
	expanding map[string]bool
}

// varPrefix scopes subgraph-node attrs that seed a child var.
const varPrefix = "var."

// Apply expands every subgraph node in g.
func (s Subgraph) Apply(g *graph.Graph) (*graph.Graph, error) {
	if s.expanding == nil {
		s.expanding = map[string]bool{}
	}
	for {
		id := findSubgraphNode(g)
		if id == "" {
			return g, nil
		}
		if err := s.expandNode(g, id); err != nil {
			return nil, err
		}
	}
}

func findSubgraphNode(g *graph.Graph) string {
	for _, id := range g.NodeOrder {
		if g.Nodes[id].Attrs["type"] == "subgraph" {
			return id
		}
	}
	return ""
}

func (s Subgraph) expandNode(g *graph.Graph, id string) error {
	node := g.Nodes[id]
	ref := node.Attrs["graph_ref"]
	if ref == "" {
		return fmt.Errorf("subgraph node %q: missing graph_ref", id)
	}
	path := ref
	if !filepath.IsAbs(path) && s.BaseDir != "" {
		path = filepath.Join(s.BaseDir, path)
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		path = abs
	}
	if s.expanding[path] {
		return fmt.Errorf("subgraph node %q: cyclic graph_ref %q", id, ref)
	}

	child, err := s.loadChild(path, id, node)
	if err != nil {
		return err
	}

	// Identify the child's splice points: the start node, its successor
	// entries, the exit nodes, and the pre-exit nodes whose outcome
	// drives the parent's outgoing conditions.
	childStart := findStart(child)
	if childStart == "" {
		return fmt.Errorf("subgraph node %q: child %q has no start node", id, ref)
	}
	terminal := func(cid string) bool { return isTerminal(cid, child.Nodes[cid]) }
	var entries []string
	for _, e := range child.OutgoingEdges(childStart) {
		entries = append(entries, prefixed(id, e.To))
	}
	if len(entries) == 0 {
		return fmt.Errorf("subgraph node %q: child start has no outgoing edges", id)
	}

	// Inline the child's nodes (minus start/exit) under the prefix.
	insertAt := indexOf(g.NodeOrder, id)
	order := append([]string{}, g.NodeOrder[:insertAt]...)
	for _, cid := range child.NodeOrder {
		if cid == childStart || terminal(cid) {
			continue
		}
		nid := prefixed(id, cid)
		if _, exists := g.Nodes[nid]; exists {
			return fmt.Errorf("subgraph node %q: inlined id %q collides", id, nid)
		}
		cn := child.Nodes[cid]
		g.Nodes[nid] = &graph.Node{ID: nid, Attrs: cn.Attrs, Subgraphs: cn.Subgraphs}
		order = append(order, nid)
	}
	order = append(order, g.NodeOrder[insertAt+1:]...)
	delete(g.Nodes, id)
	g.NodeOrder = order

	// Rebuild the edge list: child-internal edges carry the prefix;
	// edges touching the child's start/exit and the parent's edges
	// into/out of the subgraph node are spliced together. preExit holds
	// the child nodes feeding its exit — their outcome drives the
	// parent's outgoing edge conditions.
	var preExit []string
	seenPre := map[string]bool{}
	for _, e := range child.Edges {
		if e.From == childStart || !terminal(e.To) {
			continue
		}
		p := prefixed(id, e.From)
		if !seenPre[p] {
			seenPre[p] = true
			preExit = append(preExit, p)
		}
	}
	var edges []*graph.Edge
	for _, e := range g.Edges {
		switch {
		case e.To == id:
			for _, entry := range entries {
				edges = append(edges, &graph.Edge{From: e.From, To: entry, Attrs: copyAttrs(e.Attrs)})
			}
		case e.From == id:
			for _, from := range preExit {
				edges = append(edges, &graph.Edge{From: from, To: e.To, Attrs: copyAttrs(e.Attrs)})
			}
		default:
			edges = append(edges, e)
		}
	}
	for _, e := range child.Edges {
		if e.From == childStart || terminal(e.To) || terminal(e.From) {
			continue
		}
		edges = append(edges, &graph.Edge{
			From: prefixed(id, e.From), To: prefixed(id, e.To), Attrs: copyAttrs(e.Attrs),
		})
	}
	g.SetEdges(edges)
	return nil
}

// loadChild parses, prompt-expands, var-substitutes, and recursively
// subgraph-expands the child pipeline.
func (s Subgraph) loadChild(path, nodeID string, node *graph.Node) (*graph.Graph, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("subgraph node %q: load graph_ref: %w", nodeID, err)
	}
	file, err := dot.Parse(string(src))
	if err != nil {
		return nil, fmt.Errorf("subgraph node %q: parse %s: %w", nodeID, path, err)
	}
	child, err := graph.Build(file)
	if err != nil {
		return nil, fmt.Errorf("subgraph node %q: build %s: %w", nodeID, path, err)
	}
	childDir := filepath.Dir(path)
	if child, err = (PromptFile{BaseDir: childDir}).Apply(child); err != nil {
		return nil, fmt.Errorf("subgraph node %q: %w", nodeID, err)
	}
	// Recurse with the cycle guard extended by this path.
	inner := Subgraph{BaseDir: childDir, expanding: map[string]bool{}}
	for p := range s.expanding {
		inner.expanding[p] = true
	}
	inner.expanding[path] = true
	if child, err = inner.Apply(child); err != nil {
		return nil, err
	}

	// var.* static substitution: replace $context.<name> in every child
	// node attr with the seeded value.
	subs := map[string]string{}
	for k, v := range node.Attrs {
		if strings.HasPrefix(k, varPrefix) {
			subs["$context."+strings.TrimPrefix(k, varPrefix)] = v
		}
	}
	if len(subs) > 0 {
		for _, cid := range child.NodeOrder {
			for ak, av := range child.Nodes[cid].Attrs {
				for ref, val := range subs {
					av = strings.ReplaceAll(av, ref, val)
				}
				child.Nodes[cid].Attrs[ak] = av
			}
		}
		for _, e := range child.Edges {
			for ak, av := range e.Attrs {
				for ref, val := range subs {
					av = strings.ReplaceAll(av, ref, val)
				}
				e.Attrs[ak] = av
			}
		}
	}
	return child, nil
}

func prefixed(nodeID, childID string) string { return nodeID + "." + childID }

func findStart(g *graph.Graph) string {
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		if n.Shape() == "Mdiamond" || id == "start" || id == "Start" {
			return id
		}
	}
	return ""
}

func isTerminal(id string, n *graph.Node) bool {
	if n == nil {
		return false
	}
	return n.Shape() == "Msquare" || id == "exit" || id == "end"
}

func indexOf(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return len(list)
}

func copyAttrs(a map[string]string) map[string]string {
	out := make(map[string]string, len(a))
	for k, v := range a {
		out[k] = v
	}
	return out
}
