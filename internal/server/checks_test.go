package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeedChecks verifies each check is seeded — from a repo's [checks]
// config when present, and a no-op default otherwise.
func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSeedChecks(t *testing.T) {
	// No cwd → every check is a no-op default.
	seed := seedChecks(nil, "")
	for _, name := range checkNames {
		if !strings.Contains(seed["check."+name], "not configured") {
			t.Errorf("check.%s = %q, want a no-op default", name, seed["check."+name])
		}
	}

	// A repo whose .attractor/config.toml sets some checks.
	dir := t.TempDir()
	mustNil(t, os.MkdirAll(filepath.Join(dir, ".attractor"), 0o755))
	mustNil(t, os.WriteFile(filepath.Join(dir, ".attractor", "config.toml"),
		[]byte("[checks]\nlint = \"golangci-lint run\"\ntest = \"go test ./...\"\n"), 0o644))
	seed = seedChecks(nil, dir)
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
