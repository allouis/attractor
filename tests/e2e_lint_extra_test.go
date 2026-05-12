package attractor_test

import (
	"testing"

	"github.com/fabro/attractor/internal/lint"
)

func TestLint_RetryTargetMustExist(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		a [prompt="x", retry_target="ghost"]
		done [shape=Msquare]
		start -> a -> done
	}`)
	diags := lint.Validate(g)
	if !hasDiag(diags, "retry_target_exists", lint.Warning) {
		t.Fatalf("expected retry_target_exists warning: %+v", diags)
	}
}

func TestLint_GoalGateMissingRetryWarns(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		a [prompt="x", goal_gate=true]
		done [shape=Msquare]
		start -> a -> done
	}`)
	diags := lint.Validate(g)
	if !hasDiag(diags, "goal_gate_has_retry", lint.Warning) {
		t.Fatalf("expected goal_gate_has_retry warning: %+v", diags)
	}
}

func TestLint_GoalGateWithGraphRetryOK(t *testing.T) {
	g := build(t, `digraph g {
		retry_target = "a"
		start [shape=Mdiamond]
		a [prompt="x", goal_gate=true]
		done [shape=Msquare]
		start -> a -> done
	}`)
	diags := lint.Validate(g)
	for _, d := range diags {
		if d.Rule == "goal_gate_has_retry" {
			t.Fatalf("unexpected warning: %+v", d)
		}
	}
}

func TestLint_BadFidelityWarns(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		a [prompt="x", fidelity="not-a-mode"]
		done [shape=Msquare]
		start -> a -> done
	}`)
	diags := lint.Validate(g)
	if !hasDiag(diags, "fidelity_valid", lint.Warning) {
		t.Fatalf("expected fidelity_valid warning: %+v", diags)
	}
}

func TestLint_GoodFidelityPasses(t *testing.T) {
	g := build(t, `digraph g {
		default_fidelity = "summary:high"
		start [shape=Mdiamond]
		a [prompt="x", fidelity="full"]
		done [shape=Msquare]
		start -> a [fidelity="compact"]
		a -> done
	}`)
	diags := lint.Validate(g)
	for _, d := range diags {
		if d.Rule == "fidelity_valid" {
			t.Fatalf("unexpected fidelity warning: %+v", d)
		}
	}
}
