package attractor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/setup"
)

// TestReviewPipeline_DispatchRunsCheckoutInCwd drives the review pipeline
// through the same path the daemon uses for POST /items/run
// (setup.Prepare with the Item's vars + a repo cwd), with a stub `gh` on
// PATH. It proves the checkout stage expands the vars and actually runs
// the command in the run's cwd — the two things a raw structure test
// can't observe (items-spec I5).
func TestReviewPipeline_DispatchRunsCheckoutInCwd(t *testing.T) {
	cwd := t.TempDir()

	// Stub `gh`: record the args it was invoked with, in the cwd it ran in.
	binDir := t.TempDir()
	stub := "#!/bin/sh\nprintf '%s' \"$*\" > \"$PWD/gh-args.txt\"\n"
	must(t, os.WriteFile(filepath.Join(binDir, "gh"), []byte(stub), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	src, err := os.ReadFile("../pipelines/review/pipeline.dot")
	must(t, err)
	baseDir, err := filepath.Abs("../pipelines/review")
	must(t, err)

	prepared, err := setup.Prepare(setup.Options{
		Source:  string(src),
		BaseDir: baseDir,
		Cwd:     cwd,
		Vars: map[string]string{
			"repo":      "owner/repo",
			"pr_number": "42",
			"url":       "https://github.com/owner/repo/pull/42",
			"title":     "Fix login",
		},
	})
	must(t, err)

	out := runPrepared(t, prepared, fake.New())
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q", out.Status, out.FailureReason)
	}
	got, err := os.ReadFile(filepath.Join(cwd, "gh-args.txt"))
	must(t, err)
	if string(got) != "pr checkout 42 --repo owner/repo" {
		t.Fatalf("gh invoked with %q, want `pr checkout 42 --repo owner/repo`", got)
	}
}

// runPrepared runs an already-prepared graph (vars expanded, prompts
// resolved, cwd defaulted) to completion with the given backend, mirroring
// the daemon's registry wiring.
func runPrepared(t *testing.T, prepared *engine.PreparedGraph, be *fake.Backend) engine.Outcome {
	t.Helper()
	registry := engine.NewRegistry()
	registry.Register("start", handler.Start{})
	registry.Register("exit", handler.Exit{})
	registry.Register("conditional", handler.Conditional{})
	registry.Register("tool", handler.Tool{})
	registry.SetDefault(handler.Codergen{Backend: be})
	eng := engine.New(engine.Config{Registry: registry, LogsRoot: t.TempDir(), RunID: "test"})
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
