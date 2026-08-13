package handler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/engine"
)

// correctableBackend implements backend.CodergenBackend plus
// backend.Continuer so tests can script both the initial turn and the
// in-session correction turns (D2).
type correctableBackend struct {
	runFn    func(env engine.HandlerEnv, prompt string) (backend.Result, error)
	contFn   func(env engine.HandlerEnv, prompt string) (backend.Result, error)
	runs     []string
	contects []string
}

func (b *correctableBackend) Run(env engine.HandlerEnv, prompt string) (backend.Result, error) {
	b.runs = append(b.runs, prompt)
	return b.runFn(env, prompt)
}

func (b *correctableBackend) Continue(env engine.HandlerEnv, prompt string) (backend.Result, error) {
	b.contects = append(b.contects, prompt)
	return b.contFn(env, prompt)
}

// An agent that forgets to write its status.json on a require_status node
// gets a correction turn in the SAME session; when the correction lands a
// valid status, the node succeeds (D2 acceptance).
func TestContractCorrection_MissingStatusCorrectedInSession(t *testing.T) {
	shortGrace(t)
	env, dir := codergenEnv(t, map[string]string{"prompt": "p", "require_status": "true"})
	be := &correctableBackend{
		runFn: func(_ engine.HandlerEnv, _ string) (backend.Result, error) {
			return backend.Result{ResponseText: "did the work, forgot the status"}, nil
		},
		contFn: func(_ engine.HandlerEnv, _ string) (backend.Result, error) {
			os.WriteFile(filepath.Join(dir, "status.json"), []byte(`{"outcome":"success","notes":"corrected"}`), 0o644)
			return backend.Result{ResponseText: "wrote it"}, nil
		},
	}
	oc := Codergen{Backend: be}.Execute(env)
	if oc.Status != engine.StatusSuccess {
		t.Fatalf("status = %v (reason %q), want success after correction", oc.Status, oc.FailureReason)
	}
	if len(be.contects) != 1 {
		t.Fatalf("Continue called %d times, want 1", len(be.contects))
	}
	// The correction prompt must name the real status.json path and what
	// was wrong — the agent should not have to guess.
	if !strings.Contains(be.contects[0], filepath.Join(dir, "status.json")) {
		t.Errorf("correction prompt lacks the status.json path: %q", be.contects[0])
	}
	// The correction turn is persisted for post-mortem debugging.
	if _, err := os.Stat(filepath.Join(dir, "correction-1.prompt.md")); err != nil {
		t.Errorf("correction-1.prompt.md not persisted: %v", err)
	}
}

// Two failed corrections fail the node loud, keeping the require_status
// machinery-miss marker so manager_loop still distinguishes harness
// failures from agent verdicts.
func TestContractCorrection_TwoFailedCorrectionsFailLoud(t *testing.T) {
	shortGrace(t)
	env, _ := codergenEnv(t, map[string]string{"prompt": "p", "require_status": "true"})
	be := &correctableBackend{
		runFn: func(_ engine.HandlerEnv, _ string) (backend.Result, error) {
			return backend.Result{ResponseText: "no status, ever"}, nil
		},
		contFn: func(_ engine.HandlerEnv, _ string) (backend.Result, error) {
			return backend.Result{ResponseText: "still no status"}, nil
		},
	}
	oc := Codergen{Backend: be}.Execute(env)
	if oc.Status != engine.StatusFail {
		t.Fatalf("status = %v, want fail after exhausted corrections", oc.Status)
	}
	if len(be.contects) != 2 {
		t.Fatalf("Continue called %d times, want exactly 2", len(be.contects))
	}
	if !isRequireStatusMiss(oc.FailureReason) {
		t.Errorf("failure reason lost the require_status marker: %q", oc.FailureReason)
	}
}

// Corrections address contract violations ONLY. An agent-authored FAIL
// verdict is a real task failure: no correction turn fires and the
// verdict passes through verbatim.
func TestContractCorrection_AgentFailVerdictIsNotCorrected(t *testing.T) {
	env, dir := codergenEnv(t, map[string]string{"prompt": "p", "require_status": "true"})
	be := &correctableBackend{
		runFn: func(_ engine.HandlerEnv, _ string) (backend.Result, error) {
			os.WriteFile(filepath.Join(dir, "status.json"), []byte(`{"outcome":"fail","failure_reason":"blocking finding"}`), 0o644)
			return backend.Result{ResponseText: "verdict written"}, nil
		},
		contFn: func(_ engine.HandlerEnv, _ string) (backend.Result, error) {
			t.Fatal("Continue must not fire for an agent-authored FAIL")
			return backend.Result{}, nil
		},
	}
	oc := Codergen{Backend: be}.Execute(env)
	if oc.Status != engine.StatusFail || oc.FailureReason != "blocking finding" {
		t.Fatalf("agent verdict not passed through: %+v", oc)
	}
	if len(be.contects) != 0 {
		t.Fatalf("Continue called %d times, want 0", len(be.contects))
	}
}

// A backend that cannot continue a session (no Continuer) keeps the old
// behavior: the require_status miss fails immediately.
func TestContractCorrection_NonContinuerFailsImmediately(t *testing.T) {
	shortGrace(t)
	env, _ := codergenEnv(t, map[string]string{"prompt": "p", "require_status": "true"})
	be := backend.Func(func(_ engine.HandlerEnv, _ string) (backend.Result, error) {
		return backend.Result{ResponseText: "no status"}, nil
	})
	oc := Codergen{Backend: be}.Execute(env)
	if oc.Status != engine.StatusFail || !isRequireStatusMiss(oc.FailureReason) {
		t.Fatalf("want immediate require_status fail, got %+v", oc)
	}
}

// The correction loop announces itself on the event stream so a watcher
// can see the contract violation and the correction attempt.
func TestContractCorrection_EmitsProgressEvents(t *testing.T) {
	shortGrace(t)
	env, dir := codergenEnv(t, map[string]string{"prompt": "p", "require_status": "true"})
	var events []engine.Event
	env.Emit = func(ev engine.Event) { events = append(events, ev) }
	be := &correctableBackend{
		runFn: func(_ engine.HandlerEnv, _ string) (backend.Result, error) {
			return backend.Result{ResponseText: "oops"}, nil
		},
		contFn: func(_ engine.HandlerEnv, _ string) (backend.Result, error) {
			os.WriteFile(filepath.Join(dir, "status.json"), []byte(`{"outcome":"success"}`), 0o644)
			return backend.Result{ResponseText: "fixed"}, nil
		},
	}
	Codergen{Backend: be}.Execute(env)
	found := false
	for _, ev := range events {
		if ev.Detail["kind"] == "contract_correction" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no contract_correction progress event emitted; events: %+v", events)
	}
}
