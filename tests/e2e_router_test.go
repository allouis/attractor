package attractor_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
)

// routerFixture builds a router-shaped graph: a conditional `classify`
// node routes the seeded item.type to one of two stack.manager_loop
// targets, each running a distinct child (child.dot's `do` node vs
// child_alt.dot's `alt` node), with an agent `triage` fallback for
// unknown types. Mirrors the shipped router (pipelines/router).
func routerFixture(t *testing.T) string {
	t.Helper()
	reviewChild, err := filepath.Abs("../testdata/pipelines/child.dot")
	must(t, err)
	implementChild, err := filepath.Abs("../testdata/pipelines/child_alt.dot")
	must(t, err)
	return fmt.Sprintf(`digraph router {
		start [shape=Mdiamond]
		classify [type="conditional"]
		review_loop [
			shape=house,
			stack.child_dotfile="%s",
			manager.poll_interval="10ms",
			manager.max_cycles=200
		]
		implement_loop [
			shape=house,
			stack.child_dotfile="%s",
			manager.poll_interval="10ms",
			manager.max_cycles=200
		]
		triage [shape=box, prompt="triage"]
		needs_design [type="tool", tool_command="true"]
		done [shape=Msquare]

		start -> classify
		classify -> review_loop    [condition="item.type=pr"]
		classify -> implement_loop [condition="item.type=issue"]
		classify -> triage         [condition="item.type=unknown"]

		triage -> review_loop    [condition="decision=review"]
		triage -> implement_loop [condition="decision=implement"]
		triage -> needs_design   [condition="decision=design"]

		review_loop    -> done
		implement_loop -> done
		needs_design   -> done
	}`, reviewChild, implementChild)
}

// TestRouter_PRRoutesToReviewChild: a seeded item.type=pr makes the
// conditional classify edge select the review manager_loop, which runs
// child.dot (its `do` node) and NOT child_alt.dot (`alt`) — R6.
func TestRouter_PRRoutesToReviewChild(t *testing.T) {
	be := fake.New()
	be.SetText("do", "review reply")
	be.SetText("alt", "impl reply")
	out, _, _ := runFixtureSeeded(t, routerFixture(t), be, nil, map[string]string{"item.type": "pr"})
	if out.Status != engine.StatusSuccess {
		t.Fatalf("router status=%s reason=%q", out.Status, out.FailureReason)
	}
	if got := be.CallCount("do"); got != 1 {
		t.Fatalf("review child `do` ran %d times, want 1 (item.type=pr routes to review)", got)
	}
	if got := be.CallCount("alt"); got != 0 {
		t.Fatalf("implement child `alt` ran %d times, want 0", got)
	}
}

// TestRouter_IssueRoutesToImplementChild: item.type=issue routes to the
// implement manager_loop (child_alt.dot's `alt`), not review — R6.
func TestRouter_IssueRoutesToImplementChild(t *testing.T) {
	be := fake.New()
	be.SetText("do", "review reply")
	be.SetText("alt", "impl reply")
	out, _, _ := runFixtureSeeded(t, routerFixture(t), be, nil, map[string]string{"item.type": "issue"})
	if out.Status != engine.StatusSuccess {
		t.Fatalf("router status=%s reason=%q", out.Status, out.FailureReason)
	}
	if got := be.CallCount("alt"); got != 1 {
		t.Fatalf("implement child `alt` ran %d times, want 1 (item.type=issue routes to implement)", got)
	}
	if got := be.CallCount("do"); got != 0 {
		t.Fatalf("review child `do` ran %d times, want 0", got)
	}
}

// TestRouter_UnknownTypeTriagesToDecision: an unknown item.type falls
// through to the agent `triage` node, whose emitted `decision` label
// routes within the same closed target set — here `review` — R6.
func TestRouter_UnknownTypeTriagesToDecision(t *testing.T) {
	be := fake.New()
	be.SetSequence("triage", fake.Step{Outcome: &engine.Outcome{
		Status:         engine.StatusSuccess,
		ContextUpdates: map[string]string{"decision": "review"},
	}})
	be.SetText("do", "review reply")
	be.SetText("alt", "impl reply")
	out, _, _ := runFixtureSeeded(t, routerFixture(t), be, nil, map[string]string{"item.type": "unknown"})
	if out.Status != engine.StatusSuccess {
		t.Fatalf("router status=%s reason=%q", out.Status, out.FailureReason)
	}
	if got := be.CallCount("triage"); got != 1 {
		t.Fatalf("triage node ran %d times, want 1 (unknown type falls through to triage)", got)
	}
	if got := be.CallCount("do"); got != 1 {
		t.Fatalf("review child `do` ran %d times, want 1 (decision=review routes to review)", got)
	}
}
