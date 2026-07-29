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
func SVG(dotSource []byte) ([]byte, error) {
	bin, err := exec.LookPath("dot")
	if err != nil {
		return nil, ErrGraphvizMissing
	}
	cmd := exec.Command(bin, "-Tsvg")
	cmd.Stdin = bytes.NewReader(graphvizSafe(dotSource))
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
		fmt.Fprintf(&b, "  %q [shape=%q, label=%q];\n", id, n.Shape(), id)
	}
	for _, e := range g.Edges {
		label := e.Label()
		if label == "" {
			label = e.Condition()
		}
		if label != "" {
			fmt.Fprintf(&b, "  %q -> %q [label=%q];\n", e.From, e.To, label)
		} else {
			fmt.Fprintf(&b, "  %q -> %q;\n", e.From, e.To)
		}
	}
	b.WriteString("}\n")
	return []byte(b.String())
}
