package attractor_test

import (
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/dot"
	"github.com/fabro/attractor/internal/graph"
	"github.com/fabro/attractor/internal/lint"
)

func build(t *testing.T, src string) *graph.Graph {
	t.Helper()
	f, err := dot.Parse(src)
	must(t, err)
	g, err := graph.Build(f)
	must(t, err)
	return g
}

func hasDiag(diags []lint.Diagnostic, rule string, sev lint.Severity) bool {
	for _, d := range diags {
		if d.Rule == rule && d.Severity == sev {
			return true
		}
	}
	return false
}

func TestLint_CleanPipelinePassesValidateOrError(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		plan [prompt="plan it"]
		done [shape=Msquare]
		start -> plan -> done
	}`)
	diags, err := lint.ValidateOrError(g)
	if err != nil {
		t.Fatalf("clean pipeline rejected: %v", err)
	}
	for _, d := range diags {
		if d.Severity == lint.Error {
			t.Fatalf("unexpected ERROR: %+v", d)
		}
	}
}

func TestLint_MissingStartIsError(t *testing.T) {
	g := build(t, `digraph g {
		a [prompt="p"]
		done [shape=Msquare]
		a -> done
	}`)
	diags := lint.Validate(g)
	if !hasDiag(diags, "start_node", lint.Error) {
		t.Fatalf("expected start_node ERROR: %+v", diags)
	}
}

func TestLint_MissingExitIsError(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		a [prompt="p"]
		start -> a
	}`)
	diags := lint.Validate(g)
	if !hasDiag(diags, "terminal_node", lint.Error) {
		t.Fatalf("expected terminal_node ERROR: %+v", diags)
	}
}

func TestLint_MultipleStartIsError(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		Start [shape=Mdiamond]
		done [shape=Msquare]
		start -> done
		Start -> done
	}`)
	diags := lint.Validate(g)
	if !hasDiag(diags, "start_node", lint.Error) {
		t.Fatalf("expected multi-start ERROR: %+v", diags)
	}
}

func TestLint_StartHasIncomingIsError(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		a [prompt="x"]
		done [shape=Msquare]
		start -> a -> done
		a -> start
	}`)
	diags := lint.Validate(g)
	if !hasDiag(diags, "start_no_incoming", lint.Error) {
		t.Fatalf("expected start_no_incoming ERROR")
	}
}

func TestLint_ExitHasOutgoingIsError(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		a [prompt="x"]
		done [shape=Msquare]
		start -> done -> a
	}`)
	diags := lint.Validate(g)
	if !hasDiag(diags, "exit_no_outgoing", lint.Error) {
		t.Fatalf("expected exit_no_outgoing ERROR")
	}
}

func TestLint_UnreachableIsError(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		a [prompt="x"]
		orphan [prompt="orphan"]
		done [shape=Msquare]
		start -> a -> done
	}`)
	// Implicit nodes only get created from edge declarations; explicitly
	// declared nodes that no edge references must trigger reachability.
	diags := lint.Validate(g)
	if !hasDiag(diags, "reachability", lint.Error) {
		t.Fatalf("expected reachability ERROR for orphan: %+v", diags)
	}
}

func TestLint_BadConditionIsError(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		a [prompt="x"]
		done [shape=Msquare]
		start -> a [condition="="]
		a -> done
	}`)
	diags := lint.Validate(g)
	if !hasDiag(diags, "condition_syntax", lint.Error) {
		t.Fatalf("expected condition_syntax ERROR: %+v", diags)
	}
}

func TestLint_GoodConditionsPass(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		a [prompt="x"]
		done [shape=Msquare]
		start -> a
		a -> done [condition="outcome=success && context.flag!=stop"]
	}`)
	diags := lint.Validate(g)
	for _, d := range diags {
		if d.Rule == "condition_syntax" {
			t.Fatalf("clean condition rejected: %+v", d)
		}
	}
}

func TestLint_MissingPromptIsWarning(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		empty
		done [shape=Msquare]
		start -> empty -> done
	}`)
	// `empty` has neither prompt nor label attribute. The Label() accessor
	// falls back to the node ID at runtime, but the lint rule inspects the
	// raw attributes — so the warning is expected.
	diags := lint.Validate(g)
	if !hasDiag(diags, "prompt_on_llm_nodes", lint.Warning) {
		t.Fatalf("expected prompt_on_llm_nodes WARNING for empty codergen node: %+v", diags)
	}
}

func TestLint_PromptOrLabelSatisfies(t *testing.T) {
	// Either prompt or label declared explicitly suffices.
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		a [label="Plan"]
		b [prompt="critique"]
		done [shape=Msquare]
		start -> a -> b -> done
	}`)
	diags := lint.Validate(g)
	for _, d := range diags {
		if d.Rule == "prompt_on_llm_nodes" {
			t.Fatalf("unexpected warning: %+v", d)
		}
	}
}

func TestLint_UnknownTypeIsWarning(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		x [type="my_custom"]
		done [shape=Msquare]
		start -> x -> done
	}`)
	diags := lint.Validate(g)
	if !hasDiag(diags, "type_known", lint.Warning) {
		t.Fatalf("expected type_known WARNING: %+v", diags)
	}
}

func TestLint_UnknownCodergenTypeIsWarning(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		x [type="codergen.exotic"]
		done [shape=Msquare]
		start -> x -> done
	}`)
	diags := lint.Validate(g)
	if !hasDiag(diags, "codergen_type_known", lint.Warning) {
		t.Fatalf("expected codergen_type_known WARNING: %+v", diags)
	}
}

func TestLint_ValidateOrErrorAggregatesErrors(t *testing.T) {
	g := build(t, `digraph g {
		a [prompt="x"]
	}`)
	_, err := lint.ValidateOrError(g)
	if err == nil {
		t.Fatal("expected aggregated error")
	}
	if !strings.Contains(err.Error(), "start_node") {
		t.Fatalf("error missing rule names: %v", err)
	}
}
