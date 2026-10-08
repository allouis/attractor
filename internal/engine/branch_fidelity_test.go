package engine

import (
	"strings"
	"testing"
)

// fanHandler mimics the parallel handler's one obligation that matters here:
// it runs its branch through env.ExecuteNode with a context clone, then
// jumps to the join node.
type fanHandler struct{}

func (fanHandler) Execute(env HandlerEnv) Outcome {
	env.ExecuteNode("lens", env.Context.Clone())
	return Outcome{Status: StatusSuccess, NextNode: "done"}
}

// recordHandler captures the preamble and fidelity a node was executed with.
type recordHandler struct {
	preamble *string
	fidelity *FidelityMode
}

func (h recordHandler) Execute(env HandlerEnv) Outcome {
	*h.preamble = env.Preamble
	*h.fidelity = env.Fidelity
	return Outcome{Status: StatusSuccess}
}

const branchFidelityGraph = `digraph d {
	start [shape=Mdiamond]
	fan [type="fan"]
	lens [type="record", fidelity="truncate"]
	done [shape=Msquare]
	start -> fan
	fan -> lens
	lens -> done
}`

// A parallel branch resolves its own fidelity (it did), but inherited the
// parent's preamble verbatim: a review lens declared fidelity="truncate" was
// handed the fan-out node's compact dump of the whole run (plan, stale tool
// output, every completed stage) instead of the two-line truncate header
// (run 60b867a00b9b, 2026-10-08). The preamble must be built from the
// branch's own fidelity.
func TestParallelBranchPreambleFollowsBranchFidelity(t *testing.T) {
	var preamble string
	var fidelity FidelityMode
	reg := NewRegistry()
	reg.Register("start", okHandler{})
	reg.Register("fan", fanHandler{})
	reg.Register("record", recordHandler{preamble: &preamble, fidelity: &fidelity})
	eng := New(Config{Registry: reg, LogsRoot: t.TempDir(), RunID: "run-branch"})

	if _, err := eng.Run(&PreparedGraph{Graph: buildGraph(t, branchFidelityGraph)}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if fidelity != FidelityTruncate {
		t.Fatalf("branch fidelity = %q, want truncate", fidelity)
	}
	if strings.Contains(preamble, "Completed stages:") {
		t.Fatalf("truncate branch got the parent's compact preamble:\n%s", preamble)
	}
	if !strings.Contains(preamble, "[Run] run-branch") {
		t.Fatalf("truncate branch preamble should be the truncate header, got:\n%s", preamble)
	}
}
