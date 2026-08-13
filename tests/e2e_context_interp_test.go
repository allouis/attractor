package attractor_test

import (
	"os"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
)

// C1: the codergen node expands $context.* placeholders from the live
// context at execute time (spec §4.5), reading values the run started
// with (seeded initial context).
func TestContextInterp_CodergenPromptFromContext(t *testing.T) {
	src := `digraph g {
		start [shape=Mdiamond]
		plan [prompt="PR #$context.pr_number"]
		done [shape=Msquare]
		start -> plan -> done
	}`
	_, _, logs := runFixtureSeeded(t, src, fake.New(), nil, map[string]string{"pr_number": "42"})
	body, err := os.ReadFile(spanPath(t, logs, "plan", "prompt.md"))
	must(t, err)
	if !strings.Contains(string(body), "PR #42") {
		t.Fatalf("context not interpolated into prompt: %q", body)
	}
}

// Fail-fast: an undefined $context key fails the node, naming the key.
func TestContextInterp_UndefinedKeyFailsNode(t *testing.T) {
	src := `digraph g {
		start [shape=Mdiamond]
		plan [prompt="value is $context.nope"]
		done [shape=Msquare]
		start -> plan -> done
	}`
	out, _, _ := runFixture(t, src, fake.New(), nil)
	if out.Status != engine.StatusFail {
		t.Fatalf("expected FAIL on undefined key, got %s", out.Status)
	}
	if !strings.Contains(out.FailureReason, "nope") {
		t.Fatalf("failure reason should name the key: %q", out.FailureReason)
	}
}

// $goal still resolves as the one spec built-in.
func TestContextInterp_GoalStillResolves(t *testing.T) {
	src := `digraph g {
		goal = "ship it"
		start [shape=Mdiamond]
		plan [prompt="Goal: $goal"]
		done [shape=Msquare]
		start -> plan -> done
	}`
	_, _, logs := runFixture(t, src, fake.New(), nil)
	body, err := os.ReadFile(spanPath(t, logs, "plan", "prompt.md"))
	must(t, err)
	if !strings.Contains(string(body), "Goal: ship it") {
		t.Fatalf("$goal not resolved: %q", body)
	}
}

// Shell syntax survives — only $context.* is touched.
func TestContextInterp_ShellVarUntouched(t *testing.T) {
	src := `digraph g {
		start [shape=Mdiamond]
		plan [prompt="run $HOME now"]
		done [shape=Msquare]
		start -> plan -> done
	}`
	_, _, logs := runFixture(t, src, fake.New(), nil)
	body, err := os.ReadFile(spanPath(t, logs, "plan", "prompt.md"))
	must(t, err)
	if !strings.Contains(string(body), "run $HOME now") {
		t.Fatalf("shell var should pass through untouched: %q", body)
	}
}
