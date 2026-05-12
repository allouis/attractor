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
)

// ErrGraphvizMissing is returned when the `dot` binary cannot be found
// on PATH. Frontends use this to decide between rendering inline,
// returning a placeholder, or surfacing an install hint.
var ErrGraphvizMissing = errors.New("graphviz `dot` not on PATH")

// SVG renders the supplied DOT source as SVG bytes. The source is
// passed through unchanged — the Attractor parser is independent of the
// renderer, so any graphviz-valid superset works (graphviz tolerates a
// broader DOT subset than Attractor accepts at execution time).
func SVG(dotSource []byte) ([]byte, error) {
	bin, err := exec.LookPath("dot")
	if err != nil {
		return nil, ErrGraphvizMissing
	}
	cmd := exec.Command(bin, "-Tsvg")
	cmd.Stdin = bytes.NewReader(dotSource)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("dot -Tsvg: %w: %s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}
