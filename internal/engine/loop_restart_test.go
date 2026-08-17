package engine

import (
	"os"
	"path/filepath"
	"testing"
)

const restartGraph = `digraph d {
	start [shape=Mdiamond]
	work [type="probe"]
	start -> work
	work -> start [loop_restart="true"]
}`

// A loop_restart must keep run.json (identity) and graph.dot (topology) at
// the run-dir top level across incarnations, so `attractor runs` / `view`
// can still list and serve the restarted run. Only per-incarnation state
// (events.jsonl) is archived under _restart_N.
func TestLoopRestartKeepsIdentityAndTopology(t *testing.T) {
	logs := t.TempDir()
	// The CLI writes graph.dot; simulate it so the archive path could (wrongly)
	// move it if it were not excluded.
	if err := os.WriteFile(filepath.Join(logs, "graph.dot"),
		[]byte("digraph pipeline {\n  start [shape=\"Mdiamond\"];\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	reg.Register("start", okHandler{})
	reg.Register("probe", okHandler{})
	eng := New(Config{Registry: reg, LogsRoot: logs, RunID: "run-restart"})
	eng.MaxLoopRestarts = 1 // restart once, then hit the cap and stop
	_, _ = eng.Run(&PreparedGraph{Graph: buildGraph(t, restartGraph)})

	// A restart happened (prior incarnation archived).
	if _, err := os.Stat(filepath.Join(logs, "_restart_1")); err != nil {
		t.Fatalf("expected an archived incarnation _restart_1: %v", err)
	}
	// run.json and graph.dot survive at the run-dir top level.
	for _, name := range []string{"run.json", "graph.dot"} {
		if _, err := os.Stat(filepath.Join(logs, name)); err != nil {
			t.Errorf("loop_restart archived %s away from the run dir top level: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(logs, "_restart_1", name)); err == nil {
			t.Errorf("%s must not be moved into the incarnation archive", name)
		}
	}
	// Per-incarnation state IS archived.
	if _, err := os.Stat(filepath.Join(logs, "_restart_1", "events.jsonl")); err != nil {
		t.Errorf("prior incarnation's events.jsonl should be archived: %v", err)
	}
}
