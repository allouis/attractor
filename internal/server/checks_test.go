package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/automation"
	"github.com/allouis/attractor/internal/config"
)

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A minimal start->done graph is a valid pipeline body.
const doneGraphSrv = "digraph{ s [shape=Mdiamond]; e [shape=Msquare]; s -> e }"

// TestSeedChecks verifies each check is seeded — from the central
// config.json keyed by the run's repo ref, and a no-op default otherwise
// (config-screen-spec C3).
func TestSeedChecks(t *testing.T) {
	// No repo and no cwd → every check is a no-op default.
	seed := seedChecks(nil, "", "")
	for _, name := range checkNames {
		if !strings.Contains(seed["check."+name], "not configured") {
			t.Errorf("check.%s = %q, want a no-op default", name, seed["check."+name])
		}
	}

	// A repo registered in the central config with some checks; the run
	// names that repo by its owner/name ref (its path is irrelevant to the
	// lookup).
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoDir := t.TempDir()
	doc := config.Document{
		Repos: map[string]config.RepoConfig{
			"a/b": {Path: repoDir, Checks: map[string]string{
				"lint": "golangci-lint run",
				"test": "go test ./...",
			}},
		},
	}
	mustNil(t, doc.Save(home))

	seed = seedChecks(nil, "a/b", "")
	if seed["check.lint"] != "golangci-lint run" {
		t.Errorf("configured lint not used: %q", seed["check.lint"])
	}
	if seed["check.test"] != "go test ./..." {
		t.Errorf("configured test not used: %q", seed["check.test"])
	}
	if !strings.Contains(seed["check.deps"], "not configured") {
		t.Errorf("unconfigured deps should default: %q", seed["check.deps"])
	}

	// No repo ref, but the run's cwd is a registered checkout: seedChecks
	// backfills the repo identity from cwd so a cwd-only dispatch (an
	// automation, a raw-dot run) keeps the checks it gated on before C3.
	seed = seedChecks(nil, "", repoDir)
	if seed["check.lint"] != "golangci-lint run" {
		t.Errorf("cwd backfill: check.lint = %q, want configured", seed["check.lint"])
	}

	// An unregistered repo ref → every check is a no-op default.
	seed = seedChecks(nil, "x/y", "")
	for _, name := range checkNames {
		if !strings.Contains(seed["check."+name], "not configured") {
			t.Errorf("unknown repo check.%s = %q, want a no-op default", name, seed["check."+name])
		}
	}
}

// checksTestServer builds a server whose runs complete immediately (via the
// recording launcher's terminal event) so a submitted run's seeded context
// can be inspected without driving a pipeline.
func checksTestServer(t *testing.T) *Server {
	t.Helper()
	var seen []string
	def := recordingLauncher{name: "default", seen: &seen}
	srv := New(Config{
		Addr: "127.0.0.1:0", LogsRoot: t.TempDir(),
		Launcher:  def,
		Launchers: map[string]Launcher{"direct": def},
	})
	mustNil(t, srv.Start())
	t.Cleanup(func() { srv.Close() })
	return srv
}

// TestSubmitSeedsChecksForRepo guards the wiring C3 exists for: submit hands
// the run's repo ref (not its cwd) to seedChecks. The repo's registered path
// is deliberately not the run's cwd, so a lookup keyed by cwd would miss —
// only keying by the repo ref threads the configured command through.
func TestSubmitSeedsChecksForRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	doc := config.Document{Repos: map[string]config.RepoConfig{
		"a/b": {Path: "/registered/elsewhere", Checks: map[string]string{"lint": "golangci-lint run"}},
	}}
	mustNil(t, doc.Save(home))

	srv := checksTestServer(t)
	cwd := t.TempDir() // not the repo's registered path
	id, err := srv.submit(doneGraphSrv, nil, cwd, "a/b", "", "", "", "", "")
	mustNil(t, err)
	waitTerminal(t, srv, id, 5*time.Second)

	run, ok := srv.registry.Get(id)
	if !ok {
		t.Fatal("run not found")
	}
	if got := run.initialContext["check.lint"]; got != "golangci-lint run" {
		t.Errorf("check.lint = %q, want the repo's configured command (keyed by ref, not cwd)", got)
	}
}

// TestFireAutomationSeedsChecks guards that an automation run keeps its
// gating checks after C3: either from an explicit `repo` var, or backfilled
// from a cwd that is a registered checkout (the pre-C3 behaviour this
// milestone must not silently drop).
func TestFireAutomationSeedsChecks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoDir := t.TempDir()
	doc := config.Document{Repos: map[string]config.RepoConfig{
		"a/b": {Path: repoDir, Checks: map[string]string{"lint": "golangci-lint run"}},
	}}
	mustNil(t, doc.Save(home))

	pipe := filepath.Join(t.TempDir(), "pipe.dot")
	mustNil(t, os.WriteFile(pipe, []byte(doneGraphSrv), 0o644))
	srv := checksTestServer(t)

	seedFor := func(a automation.Automation) map[string]string {
		id, err := srv.fireAutomation(a)
		mustNil(t, err)
		waitTerminal(t, srv, id, 5*time.Second)
		run, ok := srv.registry.Get(id)
		if !ok {
			t.Fatal("run not found")
		}
		return run.initialContext
	}

	// No repo var, but the automation's cwd is a registered checkout.
	seed := seedFor(automation.Automation{Name: "cwd", Pipeline: pipe, Cwd: repoDir})
	if got := seed["check.lint"]; got != "golangci-lint run" {
		t.Errorf("cwd-backfill: check.lint = %q, want configured", got)
	}

	// An explicit repo var reaches submit and keys the checks even when the
	// cwd is unregistered.
	seed = seedFor(automation.Automation{Name: "var", Pipeline: pipe, Cwd: "/unregistered", Vars: map[string]string{"repo": "a/b"}})
	if got := seed["check.lint"]; got != "golangci-lint run" {
		t.Errorf("repo var: check.lint = %q, want configured", got)
	}
}
