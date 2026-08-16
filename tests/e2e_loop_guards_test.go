package attractor_test

import (
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
)

// Loop guards: kill futile runs early instead of burning review/fix
// rounds on failures no agent can fix.

// LG1: a require_status miss is a MACHINERY failure. The node retries
// (bounded by its retry policy) and then fails the RUN with the
// machinery reason — it must never take the outcome=fail edge to a fix
// node, because a harness error is not a code finding.
func TestLG1_MachineryMissNeverRoutesToFix(t *testing.T) {
	src := `digraph t {
		max_node_visits=3
		start [shape=Mdiamond]
		review [prompt="verdict", require_status="true", retry_target="fix"]
		fix [prompt="fix round"]
		done [shape=Msquare]
		start -> review
		review -> done [condition="outcome=success"]
		review -> fix  [condition="outcome=fail"]
		fix -> review
	}`
	be := fake.New()
	be.SetText("review", "prose but no status.json, every time")
	be.SetText("fix", "should never run")

	out, events, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusFail {
		t.Fatalf("status = %s, want run FAIL on machinery miss", out.Status)
	}
	if !strings.Contains(out.FailureReason, "(require_status node)") {
		t.Fatalf("run failure must carry the machinery reason, got %q", out.FailureReason)
	}
	if got := be.CallCount("fix"); got != 0 {
		t.Fatalf("fix ran %d times; a machinery miss must never route to fix", got)
	}
	for _, ev := range events {
		if ev.Kind == engine.EventStageStarted && ev.NodeID == "fix" {
			t.Fatal("fix stage started; machinery miss must not route through fail edges")
		}
	}
}

// LG1: the engine's retry exhaustion must preserve the underlying
// failure reason (and its machinery class), not flatten it to a bare
// "max retries exceeded" that hides what actually went wrong.
func TestLG1_ExhaustedRetriesKeepUnderlyingReason(t *testing.T) {
	src := `digraph t {
		default_max_retries=1
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`
	be := fake.New()
	be.SetSequence("work", fake.Step{Outcome: &engine.Outcome{
		Status:        engine.StatusRetry,
		FailureReason: "distinctive underlying reason",
	}})
	out, _, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusFail {
		t.Fatalf("status = %s, want fail", out.Status)
	}
	if !strings.Contains(out.FailureReason, "distinctive underlying reason") {
		t.Fatalf("exhaustion flattened the reason: %q", out.FailureReason)
	}
}

// lg2Src is a classic review/fix loop: work fails, fix "fixes", work
// re-runs. max_node_visits is deliberately high — LG2 must fire first.
const lg2Src = `digraph t {
	max_node_visits=10
	start [shape=Mdiamond]
	work [prompt="do it"]
	fix  [prompt="fix it"]
	done [shape=Msquare]
	start -> work
	work -> done [condition="outcome=success"]
	work -> fix  [condition="outcome=fail"]
	fix -> work
}`

func failStep(reason string) fake.Step {
	return fake.Step{Outcome: &engine.Outcome{Status: engine.StatusFail, FailureReason: reason}}
}

// LG2: the same node failing with the IDENTICAL failure_reason N
// consecutive times (default 3) aborts the run — no progress is being
// made, and every further round is waste.
func TestLG2_IdenticalRepeatedFailuresAbortRun(t *testing.T) {
	be := fake.New()
	be.SetSequence("work", failStep("cannot apply patch: hunk mismatch"))
	be.SetText("fix", "pretended to fix")

	out, _, _ := runFixture(t, lg2Src, be, nil)
	if out.Status != engine.StatusFail {
		t.Fatalf("status = %s, want fail", out.Status)
	}
	if !strings.Contains(out.FailureReason, "stuck loop") {
		t.Fatalf("want stuck-loop abort, got %q", out.FailureReason)
	}
	if got := be.CallCount("work"); got != 3 {
		t.Fatalf("work ran %d times, want 3 (abort on third identical failure)", got)
	}
}

// LG2 must never touch a loop that is making progress: changing failure
// reasons mean the fix rounds are doing something.
func TestLG2_ChangingReasonsAreNotTouched(t *testing.T) {
	be := fake.New()
	be.SetSequence("work",
		failStep("finding A"),
		failStep("finding B"),
		failStep("finding C"),
		fake.Step{Text: "all findings addressed"},
	)
	be.SetText("fix", "fixed a finding")

	out, _, _ := runFixture(t, lg2Src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status = %s (reason %q), want success — changing reasons are progress", out.Status, out.FailureReason)
	}
	if got := be.CallCount("work"); got != 4 {
		t.Fatalf("work ran %d times, want 4", got)
	}
}

// LG2: the graph attr max_repeated_failures tunes the threshold.
func TestLG2_MaxRepeatedFailuresAttr(t *testing.T) {
	src := strings.Replace(lg2Src, "max_node_visits=10", "max_node_visits=10\n\tmax_repeated_failures=2", 1)
	be := fake.New()
	be.SetSequence("work", failStep("same reason"))
	be.SetText("fix", "noop")

	out, _, _ := runFixture(t, src, be, nil)
	if !strings.Contains(out.FailureReason, "stuck loop") {
		t.Fatalf("want stuck-loop abort at threshold 2, got %q", out.FailureReason)
	}
	if got := be.CallCount("work"); got != 2 {
		t.Fatalf("work ran %d times, want 2", got)
	}
}
