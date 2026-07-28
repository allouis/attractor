package attractor_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
)

// longFinding is longer than the 200-char last_response truncation window so
// the test can prove output_key captures the full, untruncated response.
var longFinding = strings.Repeat("lens finding ", 40)

// RV1: a codergen node with output_key="review.lens" exposes its full
// response text under that context key, alongside the truncated last_response.
func TestOutputKey_FullResponseInContext(t *testing.T) {
	src := `digraph g {
	    start [shape=Mdiamond]
	    gen [prompt="review", output_key="review.lens"]
	    done [shape=Msquare]
	    start -> gen
	    gen -> done
	}`
	be := fake.New()
	be.SetText("gen", longFinding)
	_, _, logs := runFixture(t, src, be, nil)

	st := readStatus(t, logs, "gen")
	if got := st.ContextUpdates["review.lens"]; got != longFinding {
		t.Fatalf("output_key not captured full response:\n got %q\nwant %q", got, longFinding)
	}
	lr := st.ContextUpdates["last_response"]
	if !strings.HasSuffix(lr, "…") || len(lr) >= len(longFinding) {
		t.Fatalf("last_response should stay truncated, got %q", lr)
	}
}

// RV1: inside a parallel branch the output_key lands in that branch's entry
// of parallel.results, so a convergence node can read every lens's finding.
func TestOutputKey_InParallelBranch(t *testing.T) {
	src := `digraph p {
	    start [shape=Mdiamond]
	    fork [shape=component]
	    wa [prompt="A", output_key="review.correctness"]
	    wb [prompt="B"]
	    join [shape=tripleoctagon, prompt="pick best"]
	    done [shape=Msquare]
	    start -> fork
	    fork -> wa
	    fork -> wb
	    wa -> join
	    wb -> join
	    join -> done
	}`
	be := fake.New()
	be.SetText("wa", longFinding)
	be.SetText("wb", "B done")
	_, _, logs := runFixture(t, src, be, nil)

	fork := readStatus(t, logs, "fork")
	raw := fork.ContextUpdates["parallel.results"]
	if raw == "" {
		t.Fatal("parallel.results missing from fork outcome")
	}
	type branchResult struct {
		Branch  string         `json:"branch"`
		Outcome engine.Outcome `json:"outcome"`
	}
	var results []branchResult
	must(t, json.Unmarshal([]byte(raw), &results))
	var found string
	for _, r := range results {
		if r.Branch == "wa" {
			found = r.Outcome.ContextUpdates["review.correctness"]
		}
	}
	if found != longFinding {
		t.Fatalf("branch output_key not in parallel.results:\n got %q\nwant %q", found, longFinding)
	}
}
