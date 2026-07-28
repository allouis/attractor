package attractor_test

import (
	"testing"

	"github.com/allouis/attractor/internal/condition"
)

func eval(t *testing.T, expr string, vals map[string]string) bool {
	t.Helper()
	e, err := condition.Parse(expr)
	must(t, err)
	return e.Evaluate(condition.MapResolver(vals))
}

func TestCondition_EmptyAlwaysTrue(t *testing.T) {
	if !eval(t, "", nil) {
		t.Fatal("empty condition should be true")
	}
}

func TestCondition_EqualSuccess(t *testing.T) {
	if !eval(t, "outcome=success", map[string]string{"outcome": "success"}) {
		t.Fatal("outcome=success should match")
	}
	if eval(t, "outcome=success", map[string]string{"outcome": "fail"}) {
		t.Fatal("outcome=success should not match fail")
	}
}

func TestCondition_NotEqual(t *testing.T) {
	if !eval(t, "context.loop_state!=exhausted", map[string]string{"context.loop_state": "running"}) {
		t.Fatal("!= should match when values differ")
	}
	if eval(t, "context.loop_state!=exhausted", map[string]string{"context.loop_state": "exhausted"}) {
		t.Fatal("!= should not match when values equal")
	}
}

func TestCondition_AndCombination(t *testing.T) {
	vals := map[string]string{
		"outcome":              "success",
		"context.tests_passed": "true",
	}
	if !eval(t, "outcome=success && context.tests_passed=true", vals) {
		t.Fatal("compound should be true")
	}
	vals["context.tests_passed"] = "false"
	if eval(t, "outcome=success && context.tests_passed=true", vals) {
		t.Fatal("compound should be false when one clause fails")
	}
}

func TestCondition_MissingKeyEmpty(t *testing.T) {
	if eval(t, "context.never_set=value", nil) {
		t.Fatal("missing keys should compare as empty string, not match `value`")
	}
	if !eval(t, "context.never_set!=value", nil) {
		t.Fatal("missing keys should compare as empty, satisfying !=")
	}
}

func TestCondition_QuotedLiteral(t *testing.T) {
	if !eval(t, `preferred_label="Fix tests"`, map[string]string{"preferred_label": "Fix tests"}) {
		t.Fatal("quoted literal failed")
	}
}

func TestCondition_BareKeyTruthy(t *testing.T) {
	if !eval(t, "context.has_diff", map[string]string{"context.has_diff": "yes"}) {
		t.Fatal("bare key truthy: present non-empty should be true")
	}
	if eval(t, "context.has_diff", map[string]string{"context.has_diff": ""}) {
		t.Fatal("bare key truthy: empty should be false")
	}
	if eval(t, "context.has_diff", map[string]string{"context.has_diff": "false"}) {
		t.Fatal("bare key truthy: 'false' should be false")
	}
}
