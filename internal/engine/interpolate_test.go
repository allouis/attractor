package engine

import (
	"strings"
	"testing"
)

func TestContextExpand(t *testing.T) {
	ctx := NewContext()
	ctx.Set("pr_number", "42")
	ctx.Set("item.type", "pr")
	ctx.Set("repo", "foo/bar")
	ctx.Set("graph.goal", "ship it")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"simple key", "PR #$context.pr_number", "PR #42"},
		{"dotted key", "type=$context.item.type", "type=pr"},
		{"goal builtin", "Goal: $goal", "Goal: ship it"},
		{"dollar escape", "Use $$goal literally", "Use $goal literally"},
		{"shell var untouched", "echo $HOME now", "echo $HOME now"},
		{"command substitution untouched", "at $(date)", "at $(date)"},
		{"lone dollar untouched", "cost $ five", "cost $ five"},
		{"goal needs word boundary", "score $goals today", "score $goals today"},
		{"trailing dot boundary", "$context.repo. done", "foo/bar. done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ctx.Expand(tc.in)
			if err != nil {
				t.Fatalf("Expand(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("Expand(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestContextExpand_UndefinedKeyFails(t *testing.T) {
	ctx := NewContext()
	_, err := ctx.Expand("value is $context.missing")
	if err == nil {
		t.Fatal("expected error for undefined $context.missing")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error should name the key: %v", err)
	}
}
