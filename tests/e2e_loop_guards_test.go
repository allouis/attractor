package attractor_test

import (
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
)

// Loop guards (docs/loop-guards-spec.md): kill futile runs early instead
// of burning review/fix rounds on failures no agent can fix.

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
