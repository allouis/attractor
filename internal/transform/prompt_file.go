package transform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/allouis/attractor/internal/graph"
)

// PromptFile resolves `prompt="@path/to/file.md"` references by reading
// the file and inlining its contents. Paths are interpreted relative to
// BaseDir (the directory of the .dot source). Absolute paths are taken
// verbatim. The transform is spec-aligned via §9.3's custom-transform
// extension point; the canonical spec doesn't define the `@` convention
// but doesn't forbid it either.
type PromptFile struct {
	BaseDir string
}

// Apply walks every node and rewrites prompts that start with `@`.
func (p PromptFile) Apply(g *graph.Graph) (*graph.Graph, error) {
	for _, id := range g.NodeOrder {
		node := g.Nodes[id]
		prompt := node.Attrs["prompt"]
		if !strings.HasPrefix(prompt, "@") {
			continue
		}
		path := strings.TrimPrefix(prompt, "@")
		if !filepath.IsAbs(path) && p.BaseDir != "" {
			path = filepath.Join(p.BaseDir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("node %q: load prompt %q: %w", id, prompt, err)
		}
		node.Attrs["prompt"] = string(data)
	}
	return g, nil
}
