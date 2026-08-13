package handler

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/runstore"
)

func toolEnv(t *testing.T, command string) engine.HandlerEnv {
	t.Helper()
	root := t.TempDir()
	n := &graph.Node{ID: "check", Attrs: map[string]string{"tool_command": command}}
	return engine.HandlerEnv{Node: n, Context: engine.NewContext(), Stage: runstore.New(filepath.Join(root, "check"))}
}

// A failing tool whose diagnostic goes to STDOUT (gofmt -l, many
// linters) must carry that output in the failure reason — a bare
// "exit status 1" leaves the downstream fix agent blind (dogfood run
// 2026-08-13: lint failed three identical rounds because the reason was
// empty).
func TestToolFailureReasonIncludesStdout(t *testing.T) {
	oc := Tool{}.Execute(toolEnv(t, "echo unformatted-file.go; exit 1"))
	if oc.Status != engine.StatusFail {
		t.Fatalf("status = %v, want fail", oc.Status)
	}
	if !strings.Contains(oc.FailureReason, "unformatted-file.go") {
		t.Fatalf("failure reason lacks the stdout diagnostic: %q", oc.FailureReason)
	}
}

// stderr keeps priority when both streams have content.
func TestToolFailureReasonPrefersStderr(t *testing.T) {
	oc := Tool{}.Execute(toolEnv(t, "echo out-noise; echo the-real-error >&2; exit 1"))
	if !strings.Contains(oc.FailureReason, "the-real-error") {
		t.Fatalf("failure reason lacks stderr: %q", oc.FailureReason)
	}
}
