// Package render produces graphical representations of Attractor
// pipelines. The default renderer shells out to graphviz `dot -Tsvg`,
// which handles all layout concerns and is preinstalled in Attractor's
// nix derivation.
package render

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/graph"
)

// ErrGraphvizMissing is returned when the `dot` binary cannot be found
// on PATH. Frontends use this to decide between rendering inline,
// returning a placeholder, or surfacing an install hint.
var ErrGraphvizMissing = errors.New("graphviz `dot` not on PATH")

// SVG renders the supplied DOT source as SVG bytes. The source is first
// re-emitted as minimal graphviz DOT (nodes with shape + label, labelled
// edges) so Attractor-only attributes — whose dotted names like
// `stack.child_dotfile` graphviz's own parser rejects — are dropped. If
// the source doesn't parse as an Attractor graph it's passed through
// unchanged, so a plain graphviz superset still renders.
// The engine selects the graphviz layout algorithm (`-K<engine>`); an empty
// engine keeps graphviz's default (dot).
func SVG(dotSource []byte, engine string) ([]byte, error) {
	return runDot(graphvizSafe(dotSource), engine)
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

// writeGraphHeader opens the digraph and writes the graph/node/edge default
// attributes shared by every rendered pipeline. compound enables cluster-aware
// edge clipping (ltail/lhead), needed only when sub-workflows are expanded.
//
// The node/edge/graph defaults shape the rendered SVG's aesthetics
// (ui-tailwind-spec T7a): rounded boxes at the UI font on smooth splines, with
// right-sized arrowheads and tidy rank/node spacing. Colours are neutral
// fallbacks for a raw (CLI-rendered) SVG; in the web UI the .graph-pane
// stylesheet repaints fill/stroke/text from the theme tokens, and paintNode
// overrides node stroke by run status — so keep them muted, not black.
func writeGraphHeader(b *strings.Builder, compound bool, rankdir string) {
	b.WriteString("digraph pipeline {\n")
	if compound {
		b.WriteString("  compound=true;\n")
	}
	b.WriteString("  rankdir=" + safeRankdir(rankdir) + ";\n")
	b.WriteString("  bgcolor=\"transparent\";\n")
	b.WriteString("  nodesep=0.35;\n  ranksep=0.55;\n  splines=spline;\n")
	b.WriteString("  node [shape=box, style=\"rounded,filled\", fillcolor=\"#f6f8fa\", " +
		"color=\"#c9ced6\", penwidth=1, fontname=\"Helvetica\", fontsize=11, margin=\"0.18,0.09\"];\n")
	b.WriteString("  edge [color=\"#8a8f98\", penwidth=1.2, arrowsize=0.7, fontname=\"Helvetica\", fontsize=10];\n")
}

// safeRankdir constrains a pipeline's rankdir to graphviz's four legal
// directions, defaulting to TB. Allow-listing keeps a pipeline attribute from
// injecting arbitrary text into the generated DOT header.
func safeRankdir(rankdir string) string {
	switch dir := strings.ToUpper(strings.TrimSpace(rankdir)); dir {
	case "LR", "RL", "BT":
		return dir
	default:
		return "TB"
	}
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
	return MinimalDOT(g)
}

// MinimalDOT re-emits a built graph as minimal graphviz-clean DOT:
// styled header, nodes with shape + label (quoted ids), labelled edges,
// Attractor-only attributes dropped. This is the rendering form, fed to
// graphviz; it is NOT re-parseable by the Attractor dot parser (the
// graphviz styling header and quoted ids are outside its subset).
func MinimalDOT(g *graph.Graph) []byte {
	var b strings.Builder
	writeGraphHeader(&b, false, g.Attrs["rankdir"])
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

// TopologyDOT is the run's persisted graph.dot: Attractor-dialect DOT the
// Attractor dot parser round-trips (bare node ids, no graphviz styling
// header) so `attractor view` can rebuild the graph — and from it the
// per-node metadata — identical to the live run. Node role round-trips
// through shape (displayShape is the inverse of graph.TypeFromShape); the
// codergen routing attributes (llm_model, thread_id) that shape cannot
// carry are emitted explicitly. render.SVG re-cleans this through
// MinimalDOT, so /graph still renders styled.
func TopologyDOT(g *graph.Graph) []byte {
	var b strings.Builder
	b.WriteString("digraph pipeline {\n")
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		fmt.Fprintf(&b, "  %s [shape=%q", id, displayShape(n))
		// An explicit type= wins over shape in graph.Node.Type(), so shape
		// alone cannot round-trip a codergen subtype (codergen.acp, …) — every
		// codergen* maps to shape=box. Persist the explicit type verbatim so
		// the re-served view reconstructs it; a shape-only node carries no
		// type= and round-trips through shape, keeping view meta == live meta.
		writeAttr(&b, "type", n.Attrs["type"])
		writeAttr(&b, "llm_model", n.Attrs["llm_model"])
		writeAttr(&b, "thread_id", n.Attrs["thread_id"])
		b.WriteString("];\n")
	}
	for _, e := range g.Edges {
		if label := edgeLabel(e); label != "" {
			fmt.Fprintf(&b, "  %s -> %s [label=%q];\n", e.From, e.To, label)
		} else {
			fmt.Fprintf(&b, "  %s -> %s;\n", e.From, e.To)
		}
	}
	b.WriteString("}\n")
	return []byte(b.String())
}

// writeAttr appends a `, key="val"` attribute when val is non-empty.
func writeAttr(b *strings.Builder, key, val string) {
	if val != "" {
		fmt.Fprintf(b, ", %s=%q", key, val)
	}
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
	case t == "subgraph":
		return "box3d" // an inlined sub-pipeline
	case strings.HasPrefix(t, "codergen"):
		return "box"
	default:
		return n.Shape()
	}
}
