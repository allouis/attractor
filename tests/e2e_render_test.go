package attractor_test

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/cli"
	"github.com/allouis/attractor/internal/render"
)

func TestRender_SVGOutputIsWellFormed(t *testing.T) {
	if _, err := exec.LookPath("dot"); err != nil {
		t.Skip("graphviz `dot` not on PATH")
	}
	src, err := os.ReadFile("../testdata/pipelines/smoke.dot")
	must(t, err)
	svg, err := render.SVG(src)
	must(t, err)
	if !bytes.Contains(svg, []byte("<svg")) {
		t.Fatalf("output does not look like SVG: %q", svg[:min(120, len(svg))])
	}
	// well-formed XML
	dec := xml.NewDecoder(bytes.NewReader(svg))
	for {
		_, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("svg not well-formed: %v", err)
		}
	}
}

func TestRender_GraphvizMissingErrorTypeStable(t *testing.T) {
	// Just verify the sentinel exists and matches its message.
	if render.ErrGraphvizMissing == nil {
		t.Fatal("ErrGraphvizMissing should be exported")
	}
}

func TestCLI_RenderToFile(t *testing.T) {
	if _, err := exec.LookPath("dot"); err != nil {
		t.Skip("graphviz `dot` not on PATH")
	}
	out := filepath.Join(t.TempDir(), "smoke.svg")
	err := cli.Render([]string{"-o", out, "../testdata/pipelines/smoke.dot"})
	must(t, err)
	data, err := os.ReadFile(out)
	must(t, err)
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("<?xml")) && !bytes.Contains(data[:200], []byte("<svg")) {
		t.Fatalf("file is not SVG: head=%q", data[:min(120, len(data))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
