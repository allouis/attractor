package handler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/runstore"
)

// A child pipeline that sets no cwd of its own must inherit the
// parent's resolved cwd — otherwise its agents run in the attractor
// process cwd and review the wrong tree (dogfood run 2026-08-13: the
// five review lenses reviewed attractor's own working copy instead of
// the target repo).
func TestManagerLoop_ChildInheritsParentCwd(t *testing.T) {
	dir := t.TempDir()
	parentCwd := filepath.Join(dir, "target-repo")
	if err := os.MkdirAll(parentCwd, 0o755); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(dir, "child.dot")
	if err := os.WriteFile(childPath, []byte(`digraph child {
		start [shape=Mdiamond]
		work [prompt="review"]
		done [shape=Msquare]
		start -> work -> done
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var sawCwd string
	be := backend.Func(func(env engine.HandlerEnv, _ string) (backend.Result, error) {
		sawCwd = env.Cwd
		return backend.Result{ResponseText: "ok"}, nil
	})
	registry := engine.NewRegistry()
	registry.Register("start", Start{})
	registry.Register("exit", Exit{})
	registry.SetDefault(Codergen{Backend: be})

	node := &graph.Node{ID: "boss", Attrs: map[string]string{
		"type":                  "stack.manager_loop",
		"stack.child_dotfile":   childPath,
		"manager.poll_interval": "5ms",
	}}
	env := engine.HandlerEnv{
		Node: node,
		// The parent graph resolved its cwd (node attr, graph attr, or
		// the run's --cwd): manager_loop receives it as env.Cwd.
		Cwd:      parentCwd,
		Graph:    &graph.Graph{Attrs: map[string]string{}},
		Context:  engine.NewContext(),
		Stage:    runstore.New(filepath.Join(dir, "boss")),
		Registry: registry,
		RunID:    "test",
	}
	out := ManagerLoop{}.Execute(env)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("child run failed: %+v", out)
	}
	if sawCwd != parentCwd {
		t.Fatalf("child agent cwd = %q, want parent cwd %q", sawCwd, parentCwd)
	}
}

// A child that declares its own cwd keeps it.
func TestManagerLoop_ChildOwnCwdWins(t *testing.T) {
	dir := t.TempDir()
	parentCwd := filepath.Join(dir, "parent-repo")
	childCwd := filepath.Join(dir, "child-repo")
	for _, d := range []string{parentCwd, childCwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	childPath := filepath.Join(dir, "child.dot")
	if err := os.WriteFile(childPath, []byte(`digraph child {
		cwd = "`+childCwd+`"
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var sawCwd string
	be := backend.Func(func(env engine.HandlerEnv, _ string) (backend.Result, error) {
		sawCwd = env.Cwd
		return backend.Result{ResponseText: "ok"}, nil
	})
	registry := engine.NewRegistry()
	registry.Register("start", Start{})
	registry.Register("exit", Exit{})
	registry.SetDefault(Codergen{Backend: be})

	node := &graph.Node{ID: "boss", Attrs: map[string]string{
		"type":                "stack.manager_loop",
		"stack.child_dotfile": childPath,
	}}
	env := engine.HandlerEnv{
		Node: node, Cwd: parentCwd,
		Graph:   &graph.Graph{Attrs: map[string]string{}},
		Context: engine.NewContext(), Stage: runstore.New(filepath.Join(dir, "boss")),
		Registry: registry, RunID: "test",
	}
	out := ManagerLoop{}.Execute(env)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("child run failed: %+v", out)
	}
	if sawCwd != childCwd {
		t.Fatalf("child agent cwd = %q, want child's own %q", sawCwd, childCwd)
	}
}
