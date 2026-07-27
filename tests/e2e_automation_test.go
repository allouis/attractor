package attractor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fabro/attractor/internal/automation"
)

func TestAutomation_Parse(t *testing.T) {
	src := `# nightly triage
pipeline = "~/.attractor/pipelines/triage/pipeline.dot"
cwd      = "/home/agent/repo-a"

[vars]
label = "bug"
limit = "10"

[trigger]
cron = "0 3 * * *"
`
	a, err := automation.Parse("nightly-triage", []byte(src))
	must(t, err)
	if a.Name != "nightly-triage" {
		t.Fatalf("name = %q", a.Name)
	}
	if a.Pipeline != "~/.attractor/pipelines/triage/pipeline.dot" {
		t.Fatalf("pipeline = %q", a.Pipeline)
	}
	if a.Cwd != "/home/agent/repo-a" {
		t.Fatalf("cwd = %q", a.Cwd)
	}
	if a.Vars["label"] != "bug" || a.Vars["limit"] != "10" {
		t.Fatalf("vars = %v", a.Vars)
	}
	if a.Cron != "0 3 * * *" {
		t.Fatalf("cron = %q", a.Cron)
	}
}

func TestAutomation_ParseRejectsBadCron(t *testing.T) {
	src := `pipeline = "p.dot"
[trigger]
cron = "not a cron"
`
	if _, err := automation.Parse("x", []byte(src)); err == nil {
		t.Fatal("expected error for invalid cron")
	}
}

func TestAutomation_ParseRequiresPipeline(t *testing.T) {
	src := `[trigger]
cron = "0 3 * * *"
`
	if _, err := automation.Parse("x", []byte(src)); err == nil {
		t.Fatal("expected error for missing pipeline")
	}
}

func TestAutomation_LoadDir(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "one.toml"), `pipeline = "a.dot"
[trigger]
cron = "0 3 * * *"
`)
	writeFile(t, filepath.Join(dir, "two.toml"), `pipeline = "b.dot"
[trigger]
cron = "*/5 * * * *"
`)
	writeFile(t, filepath.Join(dir, "ignore.txt"), "not toml")

	list, err := automation.Load(dir)
	must(t, err)
	if len(list) != 2 {
		t.Fatalf("loaded %d automations, want 2: %+v", len(list), list)
	}
	byName := map[string]automation.Automation{}
	for _, a := range list {
		byName[a.Name] = a
	}
	if byName["one"].Pipeline != "a.dot" || byName["two"].Cron != "*/5 * * * *" {
		t.Fatalf("unexpected: %+v", byName)
	}
}

func TestAutomation_LoadMissingDir(t *testing.T) {
	// A missing automations directory is not an error: yields empty list.
	list, err := automation.Load(filepath.Join(t.TempDir(), "nope"))
	must(t, err)
	if len(list) != 0 {
		t.Fatalf("want empty, got %+v", list)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, []byte(content), 0o644))
}
