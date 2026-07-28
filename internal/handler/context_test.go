package handler

import (
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/engine"
)

func TestExpandContext(t *testing.T) {
	ctx := engine.NewContext()
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
		{"trailing dot boundary", "$context.repo. done", "foo/bar. done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandContext(tc.in, ctx)
			if err != nil {
				t.Fatalf("expandContext(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("expandContext(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExpandContext_UndefinedKeyFails(t *testing.T) {
	ctx := engine.NewContext()
	_, err := expandContext("value is $context.missing", ctx)
	if err == nil {
		t.Fatal("expected error for undefined $context.missing")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error should name the key: %v", err)
	}
}
