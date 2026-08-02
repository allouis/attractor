package server

import (
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/config"
)

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestSeedChecks verifies each check is seeded — from the central
// config.json when the run's cwd matches a registered repo, and a no-op
// default otherwise (config-screen-spec C1).
func TestSeedChecks(t *testing.T) {
	// No cwd → every check is a no-op default.
	seed := seedChecks(nil, "")
	for _, name := range checkNames {
		if !strings.Contains(seed["check."+name], "not configured") {
			t.Errorf("check.%s = %q, want a no-op default", name, seed["check."+name])
		}
	}

	// A repo registered in the central config with some checks; the run's
	// cwd is that repo's checkout.
	home := t.TempDir()
	repoDir := t.TempDir()
	t.Setenv("HOME", home)
	doc := config.Document{
		Repos: map[string]config.RepoConfig{
			"a/b": {Path: repoDir, Checks: map[string]string{
				"lint": "golangci-lint run",
				"test": "go test ./...",
			}},
		},
	}
	mustNil(t, doc.Save(home))

	seed = seedChecks(nil, repoDir)
	if seed["check.lint"] != "golangci-lint run" {
		t.Errorf("configured lint not used: %q", seed["check.lint"])
	}
	if seed["check.test"] != "go test ./..." {
		t.Errorf("configured test not used: %q", seed["check.test"])
	}
	if !strings.Contains(seed["check.deps"], "not configured") {
		t.Errorf("unconfigured deps should default: %q", seed["check.deps"])
	}
}
