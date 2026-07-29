package attractor_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
)

// TestRun_DoesNotWriteToNodeWorkDir is the layer-4 runtime invariant
// behind the runstore seam: after a run whose codergen and tool nodes have
// a work dir (cwd), that work dir is byte-for-byte unchanged — Attractor
// wrote every artifact under the run dir, never into the work dir / a
// user's repo. Complements the compile-time guard (layer 1) with an
// observed-behaviour check that survives refactors the guard can't see
// (e.g. a helper that computes a cwd-relative path at runtime).
func TestRun_DoesNotWriteToNodeWorkDir(t *testing.T) {
	workDir := t.TempDir()

	src := fmt.Sprintf(`digraph t {
		start [shape=Mdiamond]
		gen  [prompt="x", cwd=%q]
		run  [type="tool", cwd=%q, tool_command="true"]
		done [shape=Msquare]
		start -> gen -> run -> done
	}`, workDir, workDir)

	out, _, logs := runFixture(t, src, fake.New(), nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("run failed: %s %q", out.Status, out.FailureReason)
	}

	// The work dir must be empty: nothing Attractor writes lands here.
	entries, err := os.ReadDir(workDir)
	must(t, err)
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("Attractor wrote into the node work dir (must stay clean): %v", names)
	}

	// ...and the artifacts DID land under the run dir, proving the writes
	// happened and were merely confined.
	for _, want := range []string{
		filepath.Join("gen", "prompt.md"),
		filepath.Join("gen", "response.md"),
		filepath.Join("run", "stdout.txt"),
	} {
		if _, err := os.Stat(filepath.Join(logs, want)); err != nil {
			t.Fatalf("expected run artifact %s under the run dir: %v", want, err)
		}
	}
}
