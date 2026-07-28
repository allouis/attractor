package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/cli"
	"github.com/allouis/attractor/internal/dot"
	graphpkg "github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/transform"
)

func TestPromptFile_LoadsContentFromDisk(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "plan.md"), []byte("Plan: do the thing\nSteps: 1, 2, 3"), 0o644))

	src := `digraph g {
		start [shape=Mdiamond]
		plan [prompt="@plan.md"]
		done [shape=Msquare]
		start -> plan -> done
	}`
	file, err := dot.Parse(src)
	must(t, err)
	g, err := graphpkg.Build(file)
	must(t, err)

	g, err = transform.Apply(g, []transform.Transform{transform.PromptFile{BaseDir: dir}})
	must(t, err)

	got := g.Nodes["plan"].Prompt()
	if !strings.Contains(got, "Plan: do the thing") {
		t.Fatalf("@path did not inline content: %q", got)
	}
	if strings.HasPrefix(got, "@") {
		t.Fatalf("@ prefix should be stripped: %q", got)
	}
}

func TestPromptFile_MissingFileFailsLoudly(t *testing.T) {
	src := `digraph g {
		start [shape=Mdiamond]
		plan [prompt="@does-not-exist.md"]
		done [shape=Msquare]
		start -> plan -> done
	}`
	file, err := dot.Parse(src)
	must(t, err)
	g, err := graphpkg.Build(file)
	must(t, err)

	_, err = transform.Apply(g, []transform.Transform{transform.PromptFile{BaseDir: t.TempDir()}})
	if err == nil {
		t.Fatal("expected error on missing prompt file")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error should mention missing path: %v", err)
	}
}

func TestPromptFile_NoOpForLiteralPrompts(t *testing.T) {
	src := `digraph g {
		start [shape=Mdiamond]
		plan [prompt="literal text"]
		done [shape=Msquare]
		start -> plan -> done
	}`
	file, err := dot.Parse(src)
	must(t, err)
	g, err := graphpkg.Build(file)
	must(t, err)
	g, err = transform.Apply(g, []transform.Transform{transform.PromptFile{BaseDir: t.TempDir()}})
	must(t, err)
	if got := g.Nodes["plan"].Prompt(); got != "literal text" {
		t.Fatalf("literal prompt mutated: %q", got)
	}
}

func TestVariableExpansion_GoalRemainsTheDefault(t *testing.T) {
	src := `digraph g {
		goal = "ship it"
		start [shape=Mdiamond]
		plan [prompt="Plan: $goal"]
		done [shape=Msquare]
		start -> plan -> done
	}`
	file, err := dot.Parse(src)
	must(t, err)
	g, err := graphpkg.Build(file)
	must(t, err)
	g, err = transform.Apply(g, []transform.Transform{transform.VariableExpansion{}})
	must(t, err)
	if got := g.Nodes["plan"].Prompt(); got != "Plan: ship it" {
		t.Fatalf("goal expansion: %q", got)
	}
}

func TestVariableExpansion_NamedVars(t *testing.T) {
	src := `digraph g {
		goal = "fix $epic_id"
		start [shape=Mdiamond]
		plan [prompt="Epic $epic_id, priority $priority. Goal: $goal"]
		done [shape=Msquare]
		start -> plan -> done
	}`
	file, err := dot.Parse(src)
	must(t, err)
	g, err := graphpkg.Build(file)
	must(t, err)
	g, err = transform.Apply(g, []transform.Transform{transform.VariableExpansion{Vars: map[string]string{
		"epic_id":  "ABC-123",
		"priority": "high",
	}}})
	must(t, err)
	got := g.Nodes["plan"].Prompt()
	if !strings.Contains(got, "Epic ABC-123") || !strings.Contains(got, "priority high") {
		t.Fatalf("named vars not expanded: %q", got)
	}
	// $goal in the graph attribute is not re-expanded by the prompt
	// transform — only the prompt is. The plan prompt referenced $goal
	// literally, which should resolve to the graph's `goal` attribute
	// value as-stored (with its own $epic_id still literal).
	if !strings.Contains(got, "fix $epic_id") && !strings.Contains(got, "Goal: fix") {
		t.Fatalf("goal substitution missing: %q", got)
	}
}

func TestVariableExpansion_UnknownVarsLeftLiteral(t *testing.T) {
	src := `digraph g {
		start [shape=Mdiamond]
		plan [prompt="Has $known but not $unknown"]
		done [shape=Msquare]
		start -> plan -> done
	}`
	file, err := dot.Parse(src)
	must(t, err)
	g, err := graphpkg.Build(file)
	must(t, err)
	g, err = transform.Apply(g, []transform.Transform{transform.VariableExpansion{Vars: map[string]string{"known": "X"}}})
	must(t, err)
	got := g.Nodes["plan"].Prompt()
	if !strings.Contains(got, "Has X") {
		t.Fatalf("known var not expanded: %q", got)
	}
	if !strings.Contains(got, "$unknown") {
		t.Fatalf("unknown var should be left verbatim: %q", got)
	}
}

func TestVariableExpansion_DollarEscape(t *testing.T) {
	src := `digraph g {
		start [shape=Mdiamond]
		plan [prompt="Use $$goal to reference"]
		done [shape=Msquare]
		start -> plan -> done
	}`
	file, err := dot.Parse(src)
	must(t, err)
	g, err := graphpkg.Build(file)
	must(t, err)
	g, err = transform.Apply(g, []transform.Transform{transform.VariableExpansion{Vars: map[string]string{"goal": "X"}}})
	must(t, err)
	got := g.Nodes["plan"].Prompt()
	if got != "Use $goal to reference" {
		t.Fatalf("$$ escape: %q", got)
	}
}

func TestCLI_PipelineByNameResolution(t *testing.T) {
	tmp := t.TempDir()
	plDir := filepath.Join(tmp, "pipelines", "demo")
	must(t, os.MkdirAll(plDir, 0o755))
	must(t, os.WriteFile(filepath.Join(plDir, "pipeline.dot"), []byte(`digraph demo {
		start [shape=Mdiamond]
		work [prompt="hi"]
		done [shape=Msquare]
		start -> work -> done
	}`), 0o644))

	// Run with the working directory inside the tmp tree so the
	// project-relative lookup ./pipelines/demo/pipeline.dot resolves.
	cwd, err := os.Getwd()
	must(t, err)
	must(t, os.Chdir(tmp))
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	logsRoot := t.TempDir()
	err = cli.Run([]string{
		"--backend", "simulation",
		"--logs", logsRoot,
		"demo",
	})
	must(t, err)
	if _, err := os.Stat(filepath.Join(logsRoot, "work", "status.json")); err != nil {
		t.Fatalf("pipeline by name did not run: %v", err)
	}
}

func TestCLI_VarFlagAndDeclaredVarsValidation(t *testing.T) {
	tmp := t.TempDir()
	must(t, os.WriteFile(filepath.Join(tmp, "pipeline.dot"), []byte(`digraph demo {
		vars = "epic_id"
		start [shape=Mdiamond]
		work [prompt="Implement $epic_id"]
		done [shape=Msquare]
		start -> work -> done
	}`), 0o644))

	// Missing -var should fail.
	if err := cli.Run([]string{
		"--backend", "simulation",
		"--logs", t.TempDir(),
		filepath.Join(tmp, "pipeline.dot"),
	}); err == nil {
		t.Fatal("expected error when declared var is missing")
	}

	// With -var, the prompt substitution lands.
	logsRoot := t.TempDir()
	err := cli.Run([]string{
		"--backend", "simulation",
		"--logs", logsRoot,
		"--var", "epic_id=ABC-123",
		filepath.Join(tmp, "pipeline.dot"),
	})
	must(t, err)
	body, err := os.ReadFile(filepath.Join(logsRoot, "work", "prompt.md"))
	must(t, err)
	if !strings.Contains(string(body), "Implement ABC-123") {
		t.Fatalf("var not expanded into prompt: %q", body)
	}
}

func TestCLI_PromptFileEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(tmp, "prompts"), 0o755))
	must(t, os.WriteFile(filepath.Join(tmp, "prompts", "plan.md"),
		[]byte("Plan: $task"), 0o644))
	must(t, os.WriteFile(filepath.Join(tmp, "pipeline.dot"), []byte(`digraph demo {
		vars = "task"
		start [shape=Mdiamond]
		plan [prompt="@prompts/plan.md"]
		done [shape=Msquare]
		start -> plan -> done
	}`), 0o644))

	logsRoot := t.TempDir()
	err := cli.Run([]string{
		"--backend", "simulation",
		"--logs", logsRoot,
		"--var", "task=ship the thing",
		filepath.Join(tmp, "pipeline.dot"),
	})
	must(t, err)
	body, err := os.ReadFile(filepath.Join(logsRoot, "plan", "prompt.md"))
	must(t, err)
	if !strings.Contains(string(body), "Plan: ship the thing") {
		t.Fatalf("@path + $var integration: %q", body)
	}
}
