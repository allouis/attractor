package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/setup"
)

// TestSetup_PrepareInlinesFileAndExpandsVars exercises the shared setup
// path: an @file prompt is inlined relative to BaseDir and $var
// placeholders are expanded, in one pass.
func TestSetup_PrepareInlinesFileAndExpandsVars(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "plan.md"), []byte("Plan: $task"), 0o644))

	src := `digraph g {
		vars = "task"
		start [shape=Mdiamond]
		plan [prompt="@plan.md"]
		done [shape=Msquare]
		start -> plan -> done
	}`
	prepared, err := setup.Prepare(setup.Options{
		Source:  src,
		Vars:    map[string]string{"task": "ship it"},
		BaseDir: dir,
	})
	must(t, err)
	got := prepared.Graph.Nodes["plan"].Prompt()
	if !strings.Contains(got, "Plan: ship it") {
		t.Fatalf("@file + $var not resolved: %q", got)
	}
}

// TestSetup_PrepareMissingDeclaredVar fails when a declared var has no
// supplied value, matching the CLI's hard-error behaviour.
func TestSetup_PrepareMissingDeclaredVar(t *testing.T) {
	src := `digraph g {
		vars = "task"
		start [shape=Mdiamond]
		plan [prompt="Do $task"]
		done [shape=Msquare]
		start -> plan -> done
	}`
	_, err := setup.Prepare(setup.Options{Source: src})
	if err == nil {
		t.Fatal("expected error when declared var missing")
	}
	if !strings.Contains(err.Error(), "task") {
		t.Fatalf("error should name the missing var: %v", err)
	}
}

// TestSetup_CwdDefaultsGraphAttr sets the graph-level cwd default from
// Options.Cwd only when the graph does not already declare one.
func TestSetup_CwdDefaultsGraphAttr(t *testing.T) {
	src := `digraph g {
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`
	prepared, err := setup.Prepare(setup.Options{Source: src, Cwd: "/repo/a"})
	must(t, err)
	if got := prepared.Graph.Attrs["cwd"]; got != "/repo/a" {
		t.Fatalf("cwd default not applied: %q", got)
	}
}

// TestSetup_CwdDoesNotOverrideGraphAttr keeps an explicit graph cwd,
// since node/graph attrs win over the payload default.
func TestSetup_CwdDoesNotOverrideGraphAttr(t *testing.T) {
	src := `digraph g {
		cwd = "/explicit"
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`
	prepared, err := setup.Prepare(setup.Options{Source: src, Cwd: "/repo/a"})
	must(t, err)
	if got := prepared.Graph.Attrs["cwd"]; got != "/explicit" {
		t.Fatalf("explicit graph cwd overridden: %q", got)
	}
}
