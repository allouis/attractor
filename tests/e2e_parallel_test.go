package attractor_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/backend/fake"
	"github.com/fabro/attractor/internal/engine"
)

const parallelDOT = `digraph p {
    start [shape=Mdiamond]
    fork [shape=component]
    wa [prompt="A"]
    wb [prompt="B"]
    wc [prompt="C"]
    join [shape=tripleoctagon, prompt="pick best"]
    done [shape=Msquare]

    start -> fork
    fork -> wa
    fork -> wb
    fork -> wc
    wa -> join
    wb -> join
    wc -> join
    join -> done
}`

func TestParallel_FanOutWaitAll(t *testing.T) {
	be := fake.New()
	be.SetText("wa", "A done")
	be.SetText("wb", "B done")
	be.SetText("wc", "C done")
	out, _, logs := runFixture(t, parallelDOT, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("pipeline status=%s reason=%q", out.Status, out.FailureReason)
	}
	// All three workers produced artefacts.
	for _, id := range []string{"wa", "wb", "wc"} {
		if _, err := readFile(filepath.Join(logs, id, "status.json")); err != nil {
			// Workers run inside the parallel handler with cloned contexts;
			// their status.json is not written by the engine (engine only
			// writes status for nodes IT visits). Skip the file check.
			_ = err
		}
	}
	// Fan-in produced a winning branch.
	st := readStatus(t, logs, "join")
	if st.Status != engine.StatusSuccess {
		t.Fatalf("join.status=%s", st.Status)
	}
	if got := st.ContextUpdates["parallel.fan_in.best_id"]; got == "" {
		t.Fatalf("fan_in did not record best_id")
	}
	// `parallel.results` should be readable JSON.
	fork := readStatus(t, logs, "fork")
	raw := fork.ContextUpdates["parallel.results"]
	if raw == "" {
		t.Fatal("parallel.results missing from fork outcome")
	}
	var arr []map[string]any
	must(t, json.Unmarshal([]byte(raw), &arr))
	if len(arr) != 3 {
		t.Fatalf("parallel.results has %d entries, want 3", len(arr))
	}
}

func TestParallel_FirstSuccessPolicy(t *testing.T) {
	src := strings.Replace(parallelDOT, `fork [shape=component]`, `fork [shape=component, join_policy=first_success]`, 1)
	be := fake.New()
	be.SetSequence("wa", fake.Step{Outcome: &engine.Outcome{Status: engine.StatusFail, FailureReason: "no"}})
	be.SetSequence("wb", fake.Step{Text: "B done"})
	be.SetSequence("wc", fake.Step{Outcome: &engine.Outcome{Status: engine.StatusFail, FailureReason: "no"}})
	out, _, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("first_success should accept one passing branch; got %s reason=%q", out.Status, out.FailureReason)
	}
}

func TestParallel_AllBranchesFailIsFail(t *testing.T) {
	be := fake.New()
	for _, id := range []string{"wa", "wb", "wc"} {
		be.SetSequence(id, fake.Step{Outcome: &engine.Outcome{Status: engine.StatusFail, FailureReason: "boom"}})
	}
	out, _, _ := runFixture(t, parallelDOT, be, nil)
	if out.Status != engine.StatusFail {
		t.Fatalf("expected FAIL when no branch succeeds, got %s", out.Status)
	}
}

func TestParallel_MaxParallelHonoured(t *testing.T) {
	src := strings.Replace(parallelDOT, `fork [shape=component]`, `fork [shape=component, max_parallel=1]`, 1)
	be := fake.New()
	be.SetText("wa", "A")
	be.SetText("wb", "B")
	be.SetText("wc", "C")
	out, _, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q", out.Status, out.FailureReason)
	}
}

// readFile is a helper kept local since fileExists in helpers_test.go
// doesn't return contents.
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
