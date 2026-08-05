package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/engine"
)

// writeRunDir seeds a run's on-disk state (manifest.json + events.jsonl) the
// way a completed/crashed daemon would leave it, so a fresh newRunRegistry can
// reload it — the daemon-restart scenario (T9a).
func writeRunDir(t *testing.T, base, id string, status RunStatus, evs []engine.Event) {
	t.Helper()
	dir := filepath.Join(base, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		line, _ := json.Marshal(ev)
		f.Write(append(line, '\n'))
	}
	f.Close()
	m := Manifest{ID: id, Status: status, LogsRoot: dir}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// nodeStatesFromReplay mirrors the frontend graph painter (index.html
// HANDLERS): each stage event repaints its node, so the final map is what the
// graph shows after a fresh open replays the run's history.
func nodeStatesFromReplay(run *Run) map[string]string {
	state := map[string]string{}
	for ev := range run.Subscribe(0) {
		switch ev.Kind {
		case engine.EventStageStarted, engine.EventStageRetrying:
			state[ev.NodeID] = "running"
		case engine.EventStageCompleted:
			state[ev.NodeID] = "completed"
		case engine.EventStageFailed:
			state[ev.NodeID] = "failed"
		}
	}
	return state
}

// TestReloadReplaysCompletedRun is the T9a acceptance: complete a run, restart
// the daemon (a fresh registry over the same logs dir has no in-memory
// history), open it → the graph repaints every node terminal from the on-disk
// events.jsonl, nothing left executing, and the terminal event still arrives.
func TestReloadReplaysCompletedRun(t *testing.T) {
	base := t.TempDir()
	writeRunDir(t, base, "run-ok", RunCompleted, []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1},
		{Kind: engine.EventStageStarted, NodeID: "start", Seq: 2},
		{Kind: engine.EventStageCompleted, NodeID: "start", Seq: 3},
		{Kind: engine.EventStageStarted, NodeID: "work", Seq: 4},
		{Kind: engine.EventStageCompleted, NodeID: "work", Seq: 5},
		{Kind: engine.EventPipelineCompleted, Status: "success", Seq: 6},
	})

	run, ok := newRunRegistry(base).Get("run-ok")
	if !ok {
		t.Fatal("run not reloaded after restart")
	}

	var got []engine.Event
	for ev := range run.Subscribe(0) {
		got = append(got, ev)
	}
	if len(got) != 6 {
		t.Fatalf("replayed %d events, want the full 6 from disk", len(got))
	}
	if last := got[len(got)-1]; last.Kind != engine.EventPipelineCompleted {
		t.Fatalf("terminal event missing; last = %q", last.Kind)
	}

	states := nodeStatesFromReplay(run)
	for node, st := range states {
		if st != "completed" {
			t.Errorf("node %q = %q after restart, want completed", node, st)
		}
	}
}

// TestReloadResolvesInterruptedLoopedRun covers an interrupted run whose log
// ends after a nested child pipeline terminal (detail.source=child) with the
// outer looped node still started. A forwarded child terminal is NOT the run's
// own terminal, so reconstruction must look past it and still synthesize
// terminals for the still-running node — otherwise it mistakes the child's
// pipeline_failed for "log already terminal" and leaves the node stuck (T9a).
func TestReloadResolvesInterruptedLoopedRun(t *testing.T) {
	base := t.TempDir()
	writeRunDir(t, base, "run-loop-cut", RunRunning, []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1},
		{Kind: engine.EventStageStarted, NodeID: "loop", Seq: 2},
		{Kind: engine.EventPipelineStarted, NodeID: "", Seq: 3, Detail: map[string]string{"source": "child"}},
		{Kind: engine.EventPipelineFailed, NodeID: "", Seq: 4, Detail: map[string]string{"source": "child"}},
		// daemon died here: "loop" never got a terminal, no parent terminal.
	})

	run, ok := newRunRegistry(base).Get("run-loop-cut")
	if !ok {
		t.Fatal("run not reloaded after restart")
	}

	var got []engine.Event
	for ev := range run.Subscribe(0) {
		got = append(got, ev)
	}
	if last := got[len(got)-1]; last.Kind != engine.EventPipelineFailed || isChildEvent(last) {
		t.Fatalf("no synthetic parent terminal; last = %q child=%v", last.Kind, isChildEvent(last))
	}
	if st := nodeStatesFromReplay(run)["loop"]; st == "running" {
		t.Errorf("looped node stuck executing after restart")
	}
}

// TestReconstructIgnoresChildStageEvents: a daemon that dies while a nested
// child stage is running leaves the child's forwarded stage_started (tagged
// source=child) on the log with no terminal. Reconstruction must not track that
// child node into the PARENT graph — synthesizing a stage_failed for the child
// node id, emitted untagged, would paint a phantom node (or false-fail a
// same-named parent node) the frontend can't filter (BLOCKING-2). Only the
// parent's own still-running node gets resolved.
func TestReconstructIgnoresChildStageEvents(t *testing.T) {
	base := t.TempDir()
	writeRunDir(t, base, "run-child-stage", RunRunning, []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1},
		{Kind: engine.EventStageStarted, NodeID: "loop", Seq: 2},
		{Kind: engine.EventPipelineStarted, Seq: 3, Detail: map[string]string{"source": "child"}},
		{Kind: engine.EventStageStarted, NodeID: "childnode", Seq: 4, Detail: map[string]string{"source": "child"}},
		// daemon died mid child-stage; no terminals.
	})
	run, ok := newRunRegistry(base).Get("run-child-stage")
	if !ok {
		t.Fatal("run not reloaded after restart")
	}
	var got []engine.Event
	for ev := range run.Subscribe(0) {
		got = append(got, ev)
	}
	for _, ev := range got {
		if ev.Kind == engine.EventStageFailed && ev.NodeID == "childnode" && !isChildEvent(ev) {
			t.Fatalf("synthesized a phantom parent-graph stage_failed for child node %q", ev.NodeID)
		}
	}
	if st := nodeStatesFromReplay(run)["loop"]; st == "running" {
		t.Errorf("parent looped node stuck running after restart")
	}
}

// TestReconstructInterruptedLoopSynthesizesOnce: a node visited twice and still
// running when the daemon dies must get exactly ONE synthetic stage_failed, not
// one per visit. Re-entry appends the node to the resolve order each time, so
// synthesis must mark it resolved after emitting or it double-fires — inflating
// seqs and making replay non-idempotent (MED).
func TestReconstructInterruptedLoopSynthesizesOnce(t *testing.T) {
	base := t.TempDir()
	writeRunDir(t, base, "run-loop-twice", RunRunning, []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1},
		{Kind: engine.EventStageStarted, NodeID: "loop", Seq: 2},
		{Kind: engine.EventStageFailed, NodeID: "loop", Seq: 3},
		{Kind: engine.EventStageStarted, NodeID: "loop", Seq: 4},
		// daemon died on the second visit; no terminal.
	})
	run, ok := newRunRegistry(base).Get("run-loop-twice")
	if !ok {
		t.Fatal("run not reloaded after restart")
	}
	synth := 0
	for ev := range run.Subscribe(0) {
		if ev.Kind == engine.EventStageFailed && ev.NodeID == "loop" && ev.Message == "interrupted (daemon restart)" {
			synth++
		}
	}
	if synth != 1 {
		t.Fatalf("synthesized %d stage_failed for the looped node, want exactly 1", synth)
	}
}

// TestReloadResolvesInterruptedRun covers a run that was running when the
// daemon died: reload marks it cancelled, but its events.jsonl ends mid-flight
// at stage_started with no terminal event, so a naive replay leaves the node
// stuck "executing". Reconstruction must synthesize terminal events so no node
// replays to running and the stream still signals done (T9a).
func TestReloadResolvesInterruptedRun(t *testing.T) {
	base := t.TempDir()
	writeRunDir(t, base, "run-cut", RunRunning, []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1},
		{Kind: engine.EventStageStarted, NodeID: "start", Seq: 2},
		{Kind: engine.EventStageCompleted, NodeID: "start", Seq: 3},
		{Kind: engine.EventStageStarted, NodeID: "work", Seq: 4},
		// daemon died here: no stage_completed for "work", no terminal event.
	})

	run, ok := newRunRegistry(base).Get("run-cut")
	if !ok {
		t.Fatal("run not reloaded after restart")
	}
	if run.Status() != RunCancelled {
		t.Fatalf("interrupted run status = %q, want cancelled", run.Status())
	}

	var got []engine.Event
	for ev := range run.Subscribe(0) {
		got = append(got, ev)
	}
	if last := got[len(got)-1]; last.Kind != engine.EventPipelineFailed {
		t.Fatalf("no synthetic terminal event; last = %q", last.Kind)
	}

	states := nodeStatesFromReplay(run)
	if states["start"] != "completed" {
		t.Errorf("node start = %q, want completed", states["start"])
	}
	if st := states["work"]; st == "running" {
		t.Errorf("node work stuck executing after restart")
	}
}
