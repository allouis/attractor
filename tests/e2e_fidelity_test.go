package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/backend/fake"
	"github.com/fabro/attractor/internal/engine"
)

func TestFidelity_DefaultsToCompact(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		a [prompt="x"]
		done [shape=Msquare]
		start -> a -> done
	}`)
	got := engine.ResolveFidelity(nil, g.Nodes["a"], g)
	if got != engine.FidelityCompact {
		t.Fatalf("default fidelity=%q, want compact", got)
	}
}

func TestFidelity_GraphDefaultRespected(t *testing.T) {
	g := build(t, `digraph g {
		default_fidelity = "summary:high"
		start [shape=Mdiamond]
		a [prompt="x"]
		done [shape=Msquare]
		start -> a -> done
	}`)
	got := engine.ResolveFidelity(nil, g.Nodes["a"], g)
	if got != engine.FidelitySummaryHigh {
		t.Fatalf("graph-default fidelity=%q", got)
	}
}

func TestFidelity_NodeOverridesGraph(t *testing.T) {
	g := build(t, `digraph g {
		default_fidelity = "summary:high"
		start [shape=Mdiamond]
		a [prompt="x", fidelity="truncate"]
		done [shape=Msquare]
		start -> a -> done
	}`)
	got := engine.ResolveFidelity(nil, g.Nodes["a"], g)
	if got != engine.FidelityTruncate {
		t.Fatalf("node override fidelity=%q", got)
	}
}

func TestFidelity_EdgeOverridesNode(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		a [prompt="x", fidelity="compact"]
		done [shape=Msquare]
		start -> a [fidelity="full"]
		a -> done
	}`)
	edge := g.OutgoingEdges("start")[0]
	got := engine.ResolveFidelity(edge, g.Nodes["a"], g)
	if got != engine.FidelityFull {
		t.Fatalf("edge override fidelity=%q", got)
	}
}

func TestFidelity_ThreadResolutionLadder(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		a [prompt="x", thread_id="planning"]
		done [shape=Msquare]
		start -> a -> done
	}`)
	if got := engine.ResolveThread(nil, g.Nodes["a"], g, "previous"); got != "planning" {
		t.Fatalf("node thread_id=%q", got)
	}

	gNoThread := build(t, `digraph g {
		start [shape=Mdiamond]
		a [prompt="x"]
		done [shape=Msquare]
		start -> a -> done
	}`)
	if got := engine.ResolveThread(nil, gNoThread.Nodes["a"], gNoThread, "prev-stage"); got != "prev-stage" {
		t.Fatalf("fallback thread=%q, want previous node id", got)
	}
}

func TestPreamble_TruncateIsMinimal(t *testing.T) {
	in := engine.PreambleInput{
		Mode:  engine.FidelityTruncate,
		Goal:  "Build it",
		RunID: "abc",
	}
	got := engine.BuildPreamble(in)
	if !strings.Contains(got, "Build it") || !strings.Contains(got, "abc") {
		t.Fatalf("truncate preamble: %q", got)
	}
}

func TestPreamble_CompactListsStages(t *testing.T) {
	in := engine.PreambleInput{
		Mode:           engine.FidelityCompact,
		Goal:           "test",
		CompletedNodes: []string{"plan", "impl"},
		NodeOutcomes: map[string]engine.Outcome{
			"plan": {Status: engine.StatusSuccess, Notes: "ok"},
			"impl": {Status: engine.StatusSuccess, Notes: "shipped"},
		},
		Context: map[string]string{"foo": "bar", "internal.x": "y"},
	}
	got := engine.BuildPreamble(in)
	if !strings.Contains(got, "plan") || !strings.Contains(got, "impl") {
		t.Fatalf("compact preamble missing stages: %q", got)
	}
	if !strings.Contains(got, "foo = bar") {
		t.Fatalf("compact preamble missing context: %q", got)
	}
	if strings.Contains(got, "internal.x") {
		t.Fatalf("compact preamble should drop internal.* keys")
	}
}

func TestPreamble_FullIsEmpty(t *testing.T) {
	got := engine.BuildPreamble(engine.PreambleInput{Mode: engine.FidelityFull, Goal: "g"})
	if got != "" {
		t.Fatalf("full fidelity preamble should be empty, got %q", got)
	}
}

func TestFidelity_PreambleLandsInCodergenPrompt(t *testing.T) {
	src := `digraph g {
		goal = "Sort a list"
		default_fidelity = "compact"
		start [shape=Mdiamond]
		plan [prompt="Plan: $goal"]
		impl [prompt="Implement plan"]
		done [shape=Msquare]
		start -> plan -> impl -> done
	}`
	be := fake.New()
	be.SetText("plan", "PLAN: split into ranges")
	be.SetText("impl", "IMPL: done")
	_, _, logs := runFixture(t, src, be, nil)
	// `impl` runs after `plan` completes, so its prompt should carry a
	// compact preamble referencing the previous stage.
	prompt, err := os.ReadFile(filepath.Join(logs, "impl", "prompt.md"))
	must(t, err)
	body := string(prompt)
	if !strings.Contains(body, "Completed stages") {
		t.Fatalf("impl prompt missing preamble: %q", body)
	}
	if !strings.Contains(body, "plan") {
		t.Fatalf("preamble missing plan stage: %q", body)
	}
	if !strings.Contains(body, "Implement plan") {
		t.Fatalf("preamble did not preserve original prompt: %q", body)
	}
}

