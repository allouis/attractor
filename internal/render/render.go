// Package render produces graphical representations of Attractor
// pipelines. The default renderer shells out to graphviz `dot -Tsvg`,
// which handles all layout concerns and is preinstalled in Attractor's
// nix derivation.
package render

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/graph"
)

// maxExpandDepth bounds sub-workflow expansion so a pipeline that references
// itself (directly or in a cycle) cannot recurse forever.
const maxExpandDepth = 6

// ErrGraphvizMissing is returned when the `dot` binary cannot be found
// on PATH. Frontends use this to decide between rendering inline,
// returning a placeholder, or surfacing an install hint.
var ErrGraphvizMissing = errors.New("graphviz `dot` not on PATH")

// engines is the allowlist of graphviz layout engines the renderer accepts.
// Frontends screen an untrusted `?engine=` against ValidEngine so no arbitrary
// flag reaches the exec'd `dot`. The empty engine means "graphviz default"
// (dot), so it is intentionally not a member.
var engines = map[string]bool{
	"dot": true, "neato": true, "fdp": true,
	"sfdp": true, "circo": true, "twopi": true,
}

// ValidEngine reports whether name is a supported graphviz layout engine.
func ValidEngine(name string) bool { return engines[name] }

// SVG renders the supplied DOT source as SVG bytes. The source is first
// re-emitted as minimal graphviz DOT (nodes with shape + label, labelled
// edges) so Attractor-only attributes — whose dotted names like
// `stack.child_dotfile` graphviz's own parser rejects — are dropped. If
// the source doesn't parse as an Attractor graph it's passed through
// unchanged, so a plain graphviz superset still renders.
// The engine selects the graphviz layout algorithm (`-K<engine>`); an empty
// engine keeps graphviz's default (dot). Callers pass a ValidEngine-screened
// value.
func SVG(dotSource []byte, engine string) ([]byte, error) {
	return runDot(graphvizSafe(dotSource), engine)
}

// SVGExpanded renders like SVG but inlines each stack.manager_loop node's
// child pipeline as a nested cluster subgraph, recursively, so the whole
// composed workflow is visible in one graph. baseDir is the directory the
// pipeline was loaded from, used to resolve relative child_dotfile paths.
func SVGExpanded(dotSource []byte, baseDir, engine string) ([]byte, error) {
	return runDot(graphvizExpanded(dotSource, baseDir), engine)
}

// dotArgs builds the graphviz argument list: -Tsvg always, prefixed with
// -K<engine> when an engine is chosen (empty = graphviz default, dot).
func dotArgs(engine string) []string {
	if engine == "" {
		return []string{"-Tsvg"}
	}
	return []string{"-K" + engine, "-Tsvg"}
}

// runDot pipes graphviz-safe DOT through `dot -Tsvg`, laid out with the given
// engine (empty = default).
func runDot(dotSrc []byte, engine string) ([]byte, error) {
	bin, err := exec.LookPath("dot")
	if err != nil {
		return nil, ErrGraphvizMissing
	}
	cmd := exec.Command(bin, dotArgs(engine)...)
	cmd.Stdin = bytes.NewReader(dotSrc)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("dot -Tsvg: %w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// graphvizSafe re-emits an Attractor pipeline as minimal graphviz DOT that
// the graphviz parser accepts: each node as its shape + name label, each
// edge with its label or condition. Attractor-specific attributes (dotted
// names, prompts, timeouts, …) are dropped — they carry no visual meaning
// and their dotted keys are a graphviz syntax error. Source that does not
// parse as an Attractor graph is returned unchanged.
func graphvizSafe(src []byte) []byte {
	file, err := dot.Parse(string(src))
	if err != nil {
		return src
	}
	g, err := graph.Build(file)
	if err != nil {
		return src
	}
	var b strings.Builder
	b.WriteString("digraph pipeline {\n  rankdir=TB;\n")
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		fmt.Fprintf(&b, "  %q [shape=%q, label=%q];\n", id, displayShape(n), id)
	}
	for _, e := range g.Edges {
		if label := edgeLabel(e); label != "" {
			fmt.Fprintf(&b, "  %q -> %q [label=%q];\n", e.From, e.To, label)
		} else {
			fmt.Fprintf(&b, "  %q -> %q;\n", e.From, e.To)
		}
	}
	b.WriteString("}\n")
	return []byte(b.String())
}

// graphvizExpanded re-emits the pipeline like graphvizSafe, but expands
// each stack.manager_loop node whose child_dotfile resolves into a nested
// cluster subgraph holding the child pipeline (recursively). Edges into or
// out of an expanded node are redirected to the child's start / exit and
// clipped at the cluster boundary (compound edges).
func graphvizExpanded(src []byte, baseDir string) []byte {
	file, err := dot.Parse(string(src))
	if err != nil {
		return src
	}
	g, err := graph.Build(file)
	if err != nil {
		return src
	}
	var b strings.Builder
	b.WriteString("digraph pipeline {\n  compound=true;\n  rankdir=TB;\n")
	emitLevel(&b, g, "", baseDir, 0, map[string]bool{})
	b.WriteString("}\n")
	return []byte(b.String())
}

// expansion records how an expanded manager_loop node was inlined so edges
// touching it can be redirected to the child's entry/exit and clipped at
// the cluster.
type expansion struct {
	cluster string
	entry   string
	exit    string
}

// emitLevel writes one graph level: nodes (expanding manager_loop children
// into clusters) then edges (rewired around expanded nodes). prefix
// namespaces node ids so nested start/done nodes don't collide.
func emitLevel(b *strings.Builder, g *graph.Graph, prefix, baseDir string, depth int, stack map[string]bool) {
	exp := map[string]expansion{}
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		full := prefix + id
		if childPath, ok := childDotfile(n, baseDir); ok && depth < maxExpandDepth && !stack[childPath] {
			if cg, cbase, ok := loadChild(childPath); ok {
				cluster := "cluster_" + sanitizeID(full)
				childPrefix := full + "/"
				fmt.Fprintf(b, "subgraph %s {\n  label=%q; style=\"rounded,dashed\"; color=\"#9aa0a6\"; fontsize=10;\n", cluster, id)
				stack[childPath] = true
				emitLevel(b, cg, childPrefix, cbase, depth+1, stack)
				delete(stack, childPath)
				b.WriteString("}\n")
				exp[id] = expansion{
					cluster: cluster,
					entry:   childPrefix + terminalNode(cg, true),
					exit:    childPrefix + terminalNode(cg, false),
				}
				continue
			}
		}
		fmt.Fprintf(b, "  %q [shape=%q, label=%q];\n", full, displayShape(n), id)
	}
	for _, e := range g.Edges {
		from, to := prefix+e.From, prefix+e.To
		var attrs []string
		if lbl := edgeLabel(e); lbl != "" {
			attrs = append(attrs, fmt.Sprintf("label=%q", lbl))
		}
		if x, ok := exp[e.From]; ok {
			from = x.exit
			attrs = append(attrs, fmt.Sprintf("ltail=%q", x.cluster))
		}
		if x, ok := exp[e.To]; ok {
			to = x.entry
			attrs = append(attrs, fmt.Sprintf("lhead=%q", x.cluster))
		}
		if len(attrs) > 0 {
			fmt.Fprintf(b, "  %q -> %q [%s];\n", from, to, strings.Join(attrs, ", "))
		} else {
			fmt.Fprintf(b, "  %q -> %q;\n", from, to)
		}
	}
}

// childDotfile returns the resolved path to a manager_loop node's child
// pipeline, or ok=false when the node is not a manager_loop or sets none.
func childDotfile(n *graph.Node, baseDir string) (string, bool) {
	if n.Type() != "stack.manager_loop" {
		return "", false
	}
	ref := n.Attrs["stack.child_dotfile"]
	if ref == "" {
		return "", false
	}
	if !filepath.IsAbs(ref) && baseDir != "" {
		ref = filepath.Join(baseDir, ref)
	}
	return ref, true
}

// loadChild reads and parses a child pipeline, returning the graph and the
// directory ITS own children resolve against.
func loadChild(path string) (*graph.Graph, string, bool) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, "", false
	}
	file, err := dot.Parse(string(src))
	if err != nil {
		return nil, "", false
	}
	g, err := graph.Build(file)
	if err != nil {
		return nil, "", false
	}
	return g, filepath.Dir(path), true
}

// terminalNode returns the child's start (entry=true) or exit (entry=false)
// node id — where edges into/out of the cluster connect.
func terminalNode(g *graph.Graph, entry bool) string {
	want := "exit"
	if entry {
		want = "start"
	}
	for _, id := range g.NodeOrder {
		if g.Nodes[id].Type() == want {
			return id
		}
	}
	if len(g.NodeOrder) == 0 {
		return ""
	}
	if entry {
		return g.NodeOrder[0]
	}
	return g.NodeOrder[len(g.NodeOrder)-1]
}

// sanitizeID makes a graphviz-safe cluster suffix from a namespaced id.
func sanitizeID(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// edgeLabel is the edge's label or, failing that, its condition.
func edgeLabel(e *graph.Edge) string {
	if l := e.Label(); l != "" {
		return l
	}
	return e.Condition()
}

// displayShape maps a node's resolved handler type to a distinct graphviz
// shape, so the rendered graph distinguishes node roles even when the
// pipeline sets `type=` explicitly and leaves `shape` at the default box.
// It is the visual inverse of graph.TypeFromShape (Attractor Appendix B).
func displayShape(n *graph.Node) string {
	t := n.Type()
	switch {
	case t == "start":
		return "Mdiamond"
	case t == "exit":
		return "Msquare"
	case t == "conditional":
		return "diamond"
	case t == "wait.human":
		return "hexagon"
	case t == "tool":
		return "parallelogram"
	case t == "parallel":
		return "component"
	case t == "parallel.fan_in":
		return "tripleoctagon"
	case t == "stack.manager_loop":
		return "box3d" // a sub-pipeline: a "stacked" box
	case strings.HasPrefix(t, "codergen"):
		return "box"
	default:
		return n.Shape()
	}
}
