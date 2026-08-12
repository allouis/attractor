package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/runstore"
)

func codergenEnv(t *testing.T, attrs map[string]string) (engine.HandlerEnv, string) {
	t.Helper()
	root := t.TempDir()
	n := &graph.Node{ID: "synth", Attrs: attrs}
	stage := runstore.New(filepath.Join(root, "synth"))
	return engine.HandlerEnv{Node: n, Context: engine.NewContext(), Stage: stage}, filepath.Join(root, "synth")
}

// The handler substitutes {stage_dir} with the real stage root before the
// prompt reaches the agent — agents must never guess the path (the
// review-core synth guessed /status.json and its FAIL verdict was lost,
// 2026-08-12 run a9dd311b).
func TestPromptStageDirSubstituted(t *testing.T) {
	env, dir := codergenEnv(t, map[string]string{"prompt": "write {stage_dir}/status.json"})
	var got string
	be := backend.Func(func(_ engine.HandlerEnv, prompt string) (backend.Result, error) {
		got = prompt
		return backend.Result{ResponseText: "ok"}, nil
	})
	Codergen{Backend: be}.Execute(env)
	if !strings.Contains(got, dir+"/status.json") {
		t.Fatalf("prompt not substituted: %q", got)
	}
	if strings.Contains(got, "{stage_dir}") {
		t.Fatalf("placeholder leaked to agent: %q", got)
	}
	pm, err := os.ReadFile(filepath.Join(dir, "prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pm), dir) {
		t.Fatalf("persisted prompt.md not substituted: %s", pm)
	}
}

// require_status=true makes a missing agent-authored status.json a FAILURE
// instead of a synthesized success — a verdict node whose verdict never
// arrived must not pass its gate.
func TestRequireStatusFailsWithoutAgentStatus(t *testing.T) {
	env, _ := codergenEnv(t, map[string]string{"prompt": "p", "require_status": "true"})
	be := backend.Func(func(_ engine.HandlerEnv, _ string) (backend.Result, error) {
		return backend.Result{ResponseText: "verdict prose, no status file"}, nil
	})
	oc := Codergen{Backend: be}.Execute(env)
	if oc.Status != engine.StatusFail {
		t.Fatalf("status = %v, want fail (no agent status on require_status node)", oc.Status)
	}
	if !strings.Contains(oc.FailureReason, "status.json") {
		t.Fatalf("failure reason should name the missing status file: %q", oc.FailureReason)
	}
}

func TestRequireStatusHonorsAgentStatus(t *testing.T) {
	env, dir := codergenEnv(t, map[string]string{"prompt": "p", "require_status": "true"})
	be := backend.Func(func(e engine.HandlerEnv, _ string) (backend.Result, error) {
		os.WriteFile(filepath.Join(dir, "status.json"), []byte(`{"outcome":"fail","failure_reason":"blocking"}`), 0o644)
		return backend.Result{ResponseText: "wrote verdict"}, nil
	})
	oc := Codergen{Backend: be}.Execute(env)
	if oc.Status != engine.StatusFail || oc.FailureReason != "blocking" {
		t.Fatalf("agent FAIL verdict not honored: %+v", oc)
	}
}
