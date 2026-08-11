package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/report"
	"github.com/allouis/attractor/internal/setup"
)

// End-to-end of report mode at the engine layer: a run with a report sink
// forwards its events to the daemon as they happen and uploads its stage
// artifacts on completion. Mirrors what `attractor run --report-to` does.
func TestRunEngineReportingForwardsEventsAndArtifacts(t *testing.T) {
	var (
		mu        sync.Mutex
		events    []engine.Event
		artifacts = map[string]string{}
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pipelines/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var ev engine.Event
		_ = json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /pipelines/{id}/artifacts/{path...}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		artifacts[r.PathValue("path")] = string(body)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rep := &reportSink{client: report.New(srv.URL, "run-xyz", "tok"), runID: "run-xyz"}
	err := runEngineReporting(prepareToolGraph(t), fake.New(), nil, t.TempDir(), false,
		map[string]string{"k": "v"}, rep, false)
	if err != nil {
		t.Fatalf("reporting run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// Events arrived and carry the daemon's run id.
	var started, completed bool
	for _, ev := range events {
		if ev.RunID != "run-xyz" {
			t.Fatalf("event run id = %q, want run-xyz", ev.RunID)
		}
		switch ev.Kind {
		case engine.EventPipelineStarted:
			started = true
		case engine.EventPipelineCompleted:
			completed = true
		}
	}
	if !started || !completed {
		t.Fatalf("missing lifecycle events: started=%v completed=%v (n=%d)", started, completed, len(events))
	}
	// The tool node's status artifact was uploaded; daemon-owned files were not.
	if _, ok := artifacts["work/status.json"]; !ok {
		t.Fatalf("work/status.json not uploaded; got %v", keys(artifacts))
	}
	if _, ok := artifacts["events.jsonl"]; ok {
		t.Fatalf("events.jsonl should not be uploaded (daemon-owned)")
	}
}

// P3: a stage's dir uploads when THAT stage completes, not at run end, so
// GET /stages/{node} serves completed stages of a still-live run. Prove it
// with a two-stage pipeline: node `a`'s status.json must reach the daemon
// before node `b` starts — impossible under a terminal-only sweep, which
// uploads everything just before pipeline_completed (after b has started).
// The terminal sweep stays the catch-all: b's own files still arrive.
func TestRunEngineReportingUploadsStageOnCompletion(t *testing.T) {
	const twoStage = `digraph d {
		start [shape=Mdiamond]
		a [type="tool", tool_command="echo a"]
		b [type="tool", tool_command="echo b"]
		done [shape=Msquare]
		start -> a
		a -> b
		b -> done
	}`
	prepared, err := setup.Prepare(setup.Options{Source: twoStage})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	var (
		mu    sync.Mutex
		order []string // ordered log of daemon-received requests
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pipelines/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		var ev engine.Event
		_ = json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		order = append(order, "event:"+string(ev.Kind)+":"+ev.NodeID)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /pipelines/{id}/artifacts/{path...}", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		order = append(order, "artifact:"+r.PathValue("path"))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rep := &reportSink{client: report.New(srv.URL, "run-2s", "tok"), runID: "run-2s"}
	if err := runEngineReporting(prepared, fake.New(), nil, t.TempDir(), false, nil, rep, false); err != nil {
		t.Fatalf("reporting run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	idxA := indexOf(order, "artifact:a/status.json")
	idxBStart := indexOf(order, "event:stage_started:b")
	if idxA < 0 {
		t.Fatalf("a/status.json never uploaded; order=%v", order)
	}
	if idxBStart < 0 {
		t.Fatalf("b never started; order=%v", order)
	}
	if idxA > idxBStart {
		t.Fatalf("a/status.json uploaded at run end, not on a's completion (idxA=%d > idxBStart=%d); order=%v", idxA, idxBStart, order)
	}
	// Terminal sweep still covers everything: b's own files arrive too.
	if indexOf(order, "artifact:b/status.json") < 0 {
		t.Fatalf("b/status.json never uploaded (terminal sweep coverage lost); order=%v", order)
	}
}

func indexOf(s []string, want string) int {
	for i, v := range s {
		if v == want {
			return i
		}
	}
	return -1
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
