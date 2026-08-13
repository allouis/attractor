package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/runstore"
)

// shortGrace shrinks the require_status grace window so poll-based tests run
// fast, restoring the production values afterward.
func shortGrace(t *testing.T) {
	t.Helper()
	ow, oi := requireStatusGraceWindow, requireStatusPollInterval
	requireStatusGraceWindow = 400 * time.Millisecond
	requireStatusPollInterval = 10 * time.Millisecond
	t.Cleanup(func() { requireStatusGraceWindow, requireStatusPollInterval = ow, oi })
}

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
	shortGrace(t)
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

// A require_status node whose agent lands its status.json shortly AFTER the
// backend turn returns (in-guest the write races the 9p mount by ~2s) must
// honor that late verdict — including a FAIL — rather than score it as missing.
func TestRequireStatusHonorsLateStatus(t *testing.T) {
	shortGrace(t)
	env, dir := codergenEnv(t, map[string]string{"prompt": "p", "require_status": "true"})
	be := backend.Func(func(_ engine.HandlerEnv, _ string) (backend.Result, error) {
		// The turn ends now; the status file lands 100ms later.
		go func() {
			time.Sleep(100 * time.Millisecond)
			os.WriteFile(filepath.Join(dir, "status.json"), []byte(`{"outcome":"fail","failure_reason":"late blocking verdict"}`), 0o644)
		}()
		return backend.Result{ResponseText: "verdict prose, status file pending"}, nil
	})
	oc := Codergen{Backend: be}.Execute(env)
	if oc.Status != engine.StatusFail || oc.FailureReason != "late blocking verdict" {
		t.Fatalf("late FAIL verdict not honored within grace window: %+v", oc)
	}
}

// The grace window is require_status-only. A node without require_status keeps
// the single immediate check and its default-success synthesis; a status.json
// that lands after the turn is NOT waited for. Documents the intentional
// asymmetry (a universal poll would slow every node).
func TestNonRequireStatusIgnoresLateStatus(t *testing.T) {
	shortGrace(t)
	env, dir := codergenEnv(t, map[string]string{"prompt": "p"})
	written := make(chan struct{})
	be := backend.Func(func(_ engine.HandlerEnv, _ string) (backend.Result, error) {
		go func() {
			time.Sleep(100 * time.Millisecond)
			os.WriteFile(filepath.Join(dir, "status.json"), []byte(`{"outcome":"fail","failure_reason":"late"}`), 0o644)
			close(written)
		}()
		return backend.Result{ResponseText: "ok"}, nil
	})
	oc := Codergen{Backend: be}.Execute(env)
	if oc.Status != engine.StatusSuccess {
		t.Fatalf("non-require_status node should default to success, got %+v", oc)
	}
	<-written // let the goroutine finish so it can't outlive the test
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

// All three require_status miss shapes carry the machinery marker and name
// the actual problem — "wrote no" misdiagnosis cost run 9afacdba seven
// review rounds whose verdicts were on disk but uppercase.
func TestRequireStatusMissReasonVariants(t *testing.T) {
	env, dir := codergenEnv(t, map[string]string{"prompt": "p", "require_status": "true"})
	_ = dir
	stage := env.Stage

	if r := requireStatusMissReason(stage); !strings.Contains(r, "wrote no") || !isRequireStatusMiss(r) {
		t.Fatalf("no-file variant wrong: %q", r)
	}
	stage.MkdirAll()
	stage.Write("status.json", []byte("{not json"))
	if r := requireStatusMissReason(stage); !strings.Contains(r, "not valid JSON") || !isRequireStatusMiss(r) {
		t.Fatalf("bad-json variant wrong: %q", r)
	}
	stage.Write("status.json", []byte(`{"outcome":"maybe"}`))
	if r := requireStatusMissReason(stage); !strings.Contains(r, `unrecognized outcome "maybe"`) || !isRequireStatusMiss(r) {
		t.Fatalf("bad-outcome variant wrong: %q", r)
	}
	// And the historical trigger: an UPPERCASE outcome now parses fine, so
	// it is NOT a miss at all.
	stage.Write("status.json", []byte(`{"outcome":"FAIL","failure_reason":"real finding"}`))
	if _, ok := readAgentStatus(stage); !ok {
		t.Fatal("uppercase outcome must parse after the case-insensitivity fix")
	}
}
