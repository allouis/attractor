package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/setup"
)

// TestReviewPipeline_DispatchExpandsDiffCmd drives the review pipeline
// through the same path the daemon uses for item dispatch (setup.Prepare
// with the Item's vars + a repo cwd). The review is diff-based — no
// checkout stage — so the dispatch-observable contract is that the
// review-core child is seeded with the CONCRETE diff command (the item's
// repo/pr_number expanded into `gh pr diff …`) and every lens sees it
// (items-spec I5).
func TestReviewPipeline_DispatchExpandsDiffCmd(t *testing.T) {
	cwd := t.TempDir()

	src, err := os.ReadFile("../pipelines/review-pr/pipeline.dot")
	must(t, err)
	baseDir, err := filepath.Abs("../pipelines/review-pr")
	must(t, err)

	prepared, err := setup.Prepare(setup.Options{
		Source:  string(src),
		BaseDir: baseDir,
		Cwd:     cwd,
	})
	must(t, err)

	// The daemon seeds the Item's vars into the run's initial context; do
	// the same here so run-start `vars=` validation sees them (C3).
	be := fake.New()
	out := runPrepared(t, prepared, be, map[string]string{
		"repo":      "owner/repo",
		"pr_number": "42",
		"url":       "https://github.com/owner/repo/pull/42",
		"title":     "Fix login",
	})
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q", out.Status, out.FailureReason)
	}
	for _, lens := range reviewCoreLenses {
		if be.CallCount(lens) < 1 {
			t.Fatalf("lens %q never ran, want the review to fan out to it", lens)
		}
	}
	if got := childPrompt(t, be, "correctness"); !strings.Contains(got, "gh pr diff 42 --repo owner/repo") {
		t.Fatalf("lens prompt = %q, want the expanded `gh pr diff 42 --repo owner/repo`", got)
	}
}

// runPrepared runs an already-prepared graph (vars expanded, prompts
// resolved, cwd defaulted) to completion with the given backend, mirroring
// the daemon's registry wiring. initialContext seeds the run's context
// with the submitted vars, as the daemon does before Run.
func runPrepared(t *testing.T, prepared *engine.PreparedGraph, be *fake.Backend, initialContext map[string]string) engine.Outcome {
	t.Helper()
	registry := engine.NewRegistry()
	registry.Register("start", handler.Start{})
	registry.Register("exit", handler.Exit{})
	registry.Register("conditional", handler.Conditional{})
	registry.Register("tool", handler.Tool{})
	registry.Register("parallel", handler.Parallel{})
	registry.Register("parallel.fan_in", handler.FanIn{})
	registry.Register("stack.manager_loop", handler.ManagerLoop{})
	registry.SetDefault(handler.Codergen{Backend: be})
	eng := engine.New(engine.Config{Registry: registry, LogsRoot: t.TempDir(), RunID: "test", InitialContext: initialContext})
	done := make(chan struct{})
	go func() {
		for range eng.Events() {
		}
		close(done)
	}()
	outcome, err := eng.Run(prepared)
	if err != nil && outcome.Status != engine.StatusFail {
		t.Fatalf("engine error: %v", err)
	}
	<-done
	return outcome
}
