package attractor_test

import (
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/interviewer"
)

// TestEngine_MaxNodeVisitsBoundsLoops: a node caught in a failure loop
// terminates the pipeline once it exceeds the graph-level visit limit,
// instead of looping forever.
func TestEngine_MaxNodeVisitsBoundsLoops(t *testing.T) {
	src := `digraph g {
		max_node_visits = 3
		max_repeated_failures = 0 // isolate the visit limit from the LG2 stuck-loop breaker
		start [shape=Mdiamond]
		work [type="tool", tool_command="false"]
		done [shape=Msquare]
		start -> work
		work -> work [condition="outcome=fail"]
		work -> done [condition="outcome=success"]
	}`
	outcome, _, _ := runFixture(t, src, nil, interviewer.AutoApprove{})
	if outcome.Status != engine.StatusFail {
		t.Fatalf("expected pipeline FAIL from visit limit, got %+v", outcome)
	}
	if !strings.Contains(outcome.FailureReason, "max_node_visits") {
		t.Fatalf("failure should name the limit, got %q", outcome.FailureReason)
	}
}

// TestEngine_NodeMaxVisitsOverridesGraph: a per-node max_visits wins
// over the graph default.
func TestEngine_NodeMaxVisitsOverridesGraph(t *testing.T) {
	src := `digraph g {
		max_node_visits = 100
		start [shape=Mdiamond]
		work [type="tool", tool_command="false", max_visits=2]
		done [shape=Msquare]
		start -> work
		work -> work [condition="outcome=fail"]
		work -> done [condition="outcome=success"]
	}`
	outcome, _, _ := runFixture(t, src, nil, interviewer.AutoApprove{})
	if outcome.Status != engine.StatusFail {
		t.Fatalf("expected pipeline FAIL from node visit limit, got %+v", outcome)
	}
	if !strings.Contains(outcome.FailureReason, "work") {
		t.Fatalf("failure should name the node, got %q", outcome.FailureReason)
	}
}

// TestEngine_NoVisitLimitByDefaultStillTerminates: without the attr,
// bounded pipelines behave as before (regression guard: success path).
func TestEngine_VisitLimitDoesNotAffectSuccessPath(t *testing.T) {
	src := `digraph g {
		max_node_visits = 3
		start [shape=Mdiamond]
		work [type="tool", tool_command="true"]
		done [shape=Msquare]
		start -> work
		work -> done [condition="outcome=success"]
	}`
	outcome, _, _ := runFixture(t, src, nil, interviewer.AutoApprove{})
	if outcome.Status != engine.StatusSuccess {
		t.Fatalf("visit limit must not affect passing runs: %+v", outcome)
	}
}
