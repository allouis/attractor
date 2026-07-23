package attractor_test

import (
	"path/filepath"
	"testing"

	"github.com/fabro/attractor/internal/cli"
)

// withHome points HOME at a temp dir for the duration of a test so
// `automations` resolves ~/.attractor/automations there.
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestCLI_AutomationsRun(t *testing.T) {
	home := withHome(t)
	dir := t.TempDir()
	dotPath := filepath.Join(dir, "p.dot")
	writeFile(t, dotPath, `digraph p {
		start [shape=Mdiamond]
		work [prompt="hi $name"]
		done [shape=Msquare]
		start -> work -> done
	}`)
	writeFile(t, filepath.Join(home, ".attractor", "automations", "greet.toml"),
		"pipeline = \""+dotPath+"\"\n[vars]\nname = \"world\"\n")

	// Default (simulation) backend: no provider config, no override.
	if err := cli.Automations([]string{"run", "greet"}); err != nil {
		t.Fatalf("automations run: %v", err)
	}
}

func TestCLI_AutomationsRunUnknown(t *testing.T) {
	withHome(t)
	if err := cli.Automations([]string{"run", "nope"}); err == nil {
		t.Fatal("expected error for unknown automation")
	}
}

func TestCLI_AutomationsList(t *testing.T) {
	home := withHome(t)
	writeFile(t, filepath.Join(home, ".attractor", "automations", "nightly.toml"),
		"pipeline = \"p.dot\"\n[trigger]\ncron = \"0 3 * * *\"\n")
	// Just exercise the happy path; output goes to stdout.
	if err := cli.Automations([]string{"list"}); err != nil {
		t.Fatalf("automations list: %v", err)
	}
}

func TestCLI_AutomationsUsage(t *testing.T) {
	if err := cli.Automations(nil); err == nil {
		t.Fatal("expected usage error with no subcommand")
	}
	if err := cli.Automations([]string{"bogus"}); err == nil {
		t.Fatal("expected error for unknown subcommand")
	}
}
