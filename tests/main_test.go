package attractor_test

import (
	"os"
	"testing"
)

// TestMain isolates HOME to a config-free temp directory for the whole
// package. Without this, tests that run `cli.Run` without an explicit
// --backend resolve the codergen backend through the developer's real
// ~/.attractor/config.json; if that config sets a default_provider, those
// tests spawn real agents (slow, non-deterministic) that write files into
// the tests/ working directory. With no config.json the fresh default
// selects no default_provider, so bare nodes fall back to simulation.
// Individual tests may still set their own HOME via t.Setenv for
// config-specific scenarios.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "attractor-test-home")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("HOME", home)
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
