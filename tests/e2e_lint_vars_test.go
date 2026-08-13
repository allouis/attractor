package attractor_test

import (
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/lint"
)

// Vars lint (local-first plan P1.3): $context.* references are
// cross-checked against vars= ∪ node outputs ∪ well-known runtime keys
// at validate time, so a typo'd key warns before the run instead of
// interpolation-failing mid-run.

func varsDiags(t *testing.T, src string) []lint.Diagnostic {
	t.Helper()
	var out []lint.Diagnostic
	for _, d := range lint.Validate(build(t, src)) {
		if d.Rule == "context_refs" {
			out = append(out, d)
		}
	}
	return out
}

func diagMentioning(diags []lint.Diagnostic, substr string) *lint.Diagnostic {
	for i, d := range diags {
		if strings.Contains(d.Message, substr) {
			return &diags[i]
		}
	}
	return nil
}

func TestLintVars_UndeclaredContextRefWarns(t *testing.T) {
	diags := varsDiags(t, `digraph g {
		vars = "repo"
		start [shape=Mdiamond]
		work [prompt="clone $context.repo and fix $context.identifer"]
		done [shape=Msquare]
		start -> work -> done
	}`)
	d := diagMentioning(diags, "identifer")
	if d == nil {
		t.Fatalf("no warning for typo'd $context.identifer; diags: %+v", diags)
	}
	if d.Severity != lint.Warning {
		t.Fatalf("severity = %v, want warning (never blocks)", d.Severity)
	}
	if diagMentioning(diags, "\"repo\"") != nil {
		t.Fatalf("declared+used var repo must not warn; diags: %+v", diags)
	}
}

func TestLintVars_DeclaredButUnusedWarns(t *testing.T) {
	diags := varsDiags(t, `digraph g {
		vars = "repo,leftover"
		start [shape=Mdiamond]
		work [prompt="clone $context.repo"]
		done [shape=Msquare]
		start -> work -> done
	}`)
	if diagMentioning(diags, "leftover") == nil {
		t.Fatalf("no warning for declared-but-unused var leftover; diags: %+v", diags)
	}
}

// output_key and the outputs= attr declare keys a node produces at
// runtime (output_key mechanically; outputs= documents keys the agent
// sets via its status.json context_updates). Downstream references to
// either must not warn.
func TestLintVars_NodeOutputsAreDeclarations(t *testing.T) {
	diags := varsDiags(t, `digraph g {
		start [shape=Mdiamond]
		plan [prompt="plan it", output_key="plan_markdown", outputs="review_base"]
		impl [prompt="implement $context.plan_markdown since $context.review_base"]
		done [shape=Msquare]
		start -> plan -> impl -> done
	}`)
	if len(diags) != 0 {
		t.Fatalf("node-produced keys must not warn; diags: %+v", diags)
	}
}

// Runtime namespaces the engine/handlers own (graph.*, stack.*,
// human.*, item.*, check.*, parallel.*) and the routing baggage keys
// are always available; referencing them must not warn.
func TestLintVars_RuntimeKeysAllowed(t *testing.T) {
	diags := varsDiags(t, `digraph g {
		start [shape=Mdiamond]
		fix [prompt="address $context.stack.child.failure_reason per $context.human.note, then run $context.check.test; last was $context.last_response"]
		done [shape=Msquare]
		start -> fix -> done
	}`)
	if len(diags) != 0 {
		t.Fatalf("runtime keys must not warn; diags: %+v", diags)
	}
}

// Bare context.X references inside condition expressions count both
// ways: they are uses of declared vars, and undeclared ones warn.
func TestLintVars_ConditionRefsCount(t *testing.T) {
	diags := varsDiags(t, `digraph g {
		vars = "kind"
		start [shape=Mdiamond]
		triage [prompt="triage the item"]
		a [prompt="a"]
		done [shape=Msquare]
		start -> triage
		triage -> a [condition="context.kind=bug"]
		triage -> done [condition="context.verdcit=pass"]
		a -> done
	}`)
	if diagMentioning(diags, "kind") != nil {
		t.Fatalf("condition use of declared var must not warn; diags: %+v", diags)
	}
	if diagMentioning(diags, "verdcit") == nil {
		t.Fatalf("undeclared condition key must warn; diags: %+v", diags)
	}
}
