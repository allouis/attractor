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
		map[string]string{"k": "v"}, rep)
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

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
