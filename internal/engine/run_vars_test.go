package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/graph"
)

type okHandler struct{}

func (okHandler) Execute(env HandlerEnv) Outcome { return Outcome{Status: StatusSuccess} }

// probeHandler records the value it sees for context key "k", proving the
// seeded var reached the live context.
type probeHandler struct{ saw *string }

func (h probeHandler) Execute(env HandlerEnv) Outcome {
	if h.saw != nil {
		*h.saw = env.Context.Get("k")
	}
	return Outcome{Status: StatusSuccess}
}

func buildGraph(t *testing.T, src string) *graph.Graph {
	t.Helper()
	f, err := dot.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g, err := graph.Build(f)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return g
}

const declaredVarGraph = `digraph d {
	vars="k"
	start [shape=Mdiamond]
	work [type="probe"]
	done [shape=Msquare]
	start -> work
	work -> done
}`

// TestRunFailsOnMissingDeclaredVar: a pipeline declaring vars="k" run with
// no k seeded fails at run-start, naming k, before any node executes (C3).
func TestRunFailsOnMissingDeclaredVar(t *testing.T) {
	reg := NewRegistry()
	reg.Register("start", okHandler{})
	reg.Register("probe", probeHandler{})
	eng := New(Config{Registry: reg, LogsRoot: t.TempDir()})

	outcome, err := eng.Run(&PreparedGraph{Graph: buildGraph(t, declaredVarGraph)})
	if err == nil {
		t.Fatal("want error for missing declared var, got nil")
	}
	if outcome.Status != StatusFail {
		t.Fatalf("status = %v, want FAIL", outcome.Status)
	}
	if !strings.Contains(outcome.FailureReason, "k") {
		t.Fatalf("reason %q does not name missing var k", outcome.FailureReason)
	}
}

// TestRunSeededDeclaredVarSucceeds: with k seeded into the initial context,
// validation passes and the node observes the value.
func TestRunSeededDeclaredVarSucceeds(t *testing.T) {
	var saw string
	reg := NewRegistry()
	reg.Register("start", okHandler{})
	reg.Register("probe", probeHandler{saw: &saw})
	eng := New(Config{Registry: reg, LogsRoot: t.TempDir(), InitialContext: map[string]string{"k": "v"}})

	outcome, err := eng.Run(&PreparedGraph{Graph: buildGraph(t, declaredVarGraph)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Status != StatusSuccess {
		t.Fatalf("status = %v, want SUCCESS: %s", outcome.Status, outcome.FailureReason)
	}
	if saw != "v" {
		t.Fatalf("probe saw k=%q, want v", saw)
	}
}

// TestRunResumeSkipsDeclaredVarValidation: `vars=` validation is a
// fresh-run start gate, not re-checked on resume. A run resumed from a
// checkpoint whose context lacks a declared key (e.g. written by a binary
// that predates context seeding) must proceed, not fail — the run was
// already admitted at its original start.
func TestRunResumeSkipsDeclaredVarValidation(t *testing.T) {
	logs := t.TempDir()
	startDone := Outcome{Status: StatusSuccess}
	startDone.Finalize()
	ckpt := Checkpoint{
		CurrentNode:    "start",
		CompletedNodes: []string{"start"},
		NodeRetries:    map[string]int{},
		ContextValues:  map[string]string{}, // no "k"
		LastOutcome:    &startDone,
		NodeOutcomes:   map[string]Outcome{"start": startDone},
	}
	data, err := json.Marshal(ckpt)
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logs, "checkpoint.json"), data, 0o644); err != nil {
		t.Fatalf("write checkpoint: %v", err)
	}

	reg := NewRegistry()
	reg.Register("start", okHandler{})
	reg.Register("probe", okHandler{})
	eng := New(Config{Registry: reg, LogsRoot: logs})

	outcome, err := eng.Run(&PreparedGraph{Graph: buildGraph(t, declaredVarGraph)})
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if outcome.Status != StatusSuccess {
		t.Fatalf("status = %v, want SUCCESS: %s", outcome.Status, outcome.FailureReason)
	}
}
