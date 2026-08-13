package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
)

// Fixes from the 2026-08-13 clean-code + DDD audits. Each test pins a
// real behavioral defect.

// Audit bug 1: every parallel branch received the PARENT node's stage
// dir — concurrent branches clobbered each other's prompt/response and
// could consume each other's status.json verdicts. Each branch must get
// its own stage seam.
func TestParallel_BranchesGetDistinctStageDirs(t *testing.T) {
	src := `digraph t {
		start [shape=Mdiamond]
		fan [shape=component]
		a [prompt="branch a"]
		b [prompt="branch b"]
		synth [prompt="merge"]
		done [shape=Msquare]
		start -> fan
		fan -> a
		fan -> b
		a -> synth
		b -> synth
		synth -> done
	}`
	stages := map[string]string{}
	be := backend.Func(func(env engine.HandlerEnv, _ string) (backend.Result, error) {
		if env.Stage != nil {
			stages[env.Node.ID] = env.Stage.Root()
		}
		// Each branch self-reports a DISTINCT verdict via the contract.
		if env.Node.ID == "a" || env.Node.ID == "b" {
			_ = os.MkdirAll(env.Stage.Root(), 0o755)
			_ = os.WriteFile(filepath.Join(env.Stage.Root(), "status.json"),
				[]byte(`{"outcome":"success","notes":"from `+env.Node.ID+`"}`), 0o644)
		}
		return backend.Result{ResponseText: "ok " + env.Node.ID}, nil
	})
	out, _, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("run failed: %+v", out)
	}
	if stages["a"] == "" || stages["a"] == stages["b"] {
		t.Fatalf("branches share a stage dir: a=%q b=%q", stages["a"], stages["b"])
	}
	if stages["a"] == stages["fan"] || stages["b"] == stages["fan"] {
		t.Fatalf("branch inherited the parallel node's own stage dir: %q", stages["fan"])
	}
}

// Audit bug 3: `outcome == fail` silently misparsed into key "outcome",
// literal "= fail" — the edge never matched and nothing warned. `==`
// now parses as equality.
func TestCondition_DoubleEqualsParsesAsEquality(t *testing.T) {
	src := `digraph t {
		max_repeated_failures=0
		start [shape=Mdiamond]
		work [prompt="x"]
		fix [prompt="fix"]
		done [shape=Msquare]
		start -> work
		work -> done [condition="outcome == success"]
		work -> fix  [condition="outcome == fail"]
		fix -> done
	}`
	be := fake.New()
	be.SetSequence("work", fake.Step{Outcome: &engine.Outcome{
		Status: engine.StatusFail, FailureReason: "boom",
	}})
	be.SetText("fix", "fixed")
	out, _, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q — the == fail edge never matched", out.Status, out.FailureReason)
	}
	if be.CallCount("fix") != 1 {
		t.Fatalf("fix ran %d times, want 1 (via the == condition)", be.CallCount("fix"))
	}
}

// Audit bug 4 (DDD #1): outcomes were recorded for goal-gate checking
// only on success, so a goal_gate node that FAILED and was routed to
// exit via an outcome=fail edge slipped through the terminal gate and
// the pipeline exited SUCCESS — precisely what spec §3.4 forbids.
func TestGoalGate_FailedGateBlocksTerminalSuccess(t *testing.T) {
	src := `digraph t {
		start [shape=Mdiamond]
		crucial [prompt="must succeed", goal_gate=true]
		done [shape=Msquare]
		start -> crucial
		crucial -> done [condition="outcome=fail"]
		crucial -> done [condition="outcome=success"]
	}`
	be := fake.New()
	be.SetSequence("crucial", fake.Step{Outcome: &engine.Outcome{
		Status: engine.StatusFail, FailureReason: "gate not met",
	}})
	out, _, _ := runFixture(t, src, be, nil)
	if out.Status == engine.StatusSuccess {
		t.Fatal("pipeline exited SUCCESS with a failed goal_gate node (§3.4)")
	}
	if !strings.Contains(out.FailureReason, "goal gate") {
		t.Fatalf("failure should name the goal gate, got %q", out.FailureReason)
	}
}

// A goal-gate failure with a retry_target re-routes at the terminal and
// can still succeed once the gate node passes.
func TestGoalGate_FailedGateRetriesViaTarget(t *testing.T) {
	src := `digraph t {
		max_repeated_failures=0
		start [shape=Mdiamond]
		crucial [prompt="flaky", goal_gate=true, retry_target="crucial"]
		done [shape=Msquare]
		start -> crucial
		crucial -> done
	}`
	be := fake.New()
	be.SetSequence("crucial",
		fake.Step{Outcome: &engine.Outcome{Status: engine.StatusFail, FailureReason: "not yet"}},
		fake.Step{Text: "made it"},
	)
	out, _, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q, want success after gate retry", out.Status, out.FailureReason)
	}
	if be.CallCount("crucial") != 2 {
		t.Fatalf("crucial ran %d times, want 2", be.CallCount("crucial"))
	}
}

// Audit bug 5 (DDD #2): a panicking handler killed the whole process —
// no terminal event, run dirs stuck "running" forever. Panics must
// become FAIL outcomes (§4.12).
func TestHandlerPanic_BecomesFailOutcome(t *testing.T) {
	src := `digraph t {
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`
	be := backend.Func(func(env engine.HandlerEnv, _ string) (backend.Result, error) {
		panic("backend exploded")
	})
	out, events, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusFail {
		t.Fatalf("status=%s, want fail from recovered panic", out.Status)
	}
	if !strings.Contains(out.FailureReason, "panic") || !strings.Contains(out.FailureReason, "backend exploded") {
		t.Fatalf("failure reason should carry the panic: %q", out.FailureReason)
	}
	terminal := false
	for _, ev := range events {
		if ev.Kind == engine.EventPipelineFailed {
			terminal = true
		}
	}
	if !terminal {
		t.Fatal("no pipeline_failed terminal event after panic")
	}
}
