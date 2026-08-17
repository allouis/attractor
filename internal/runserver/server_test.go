package runserver

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/runview"
)

// writeRun materializes a minimal run dir: run.json + events.jsonl +
// one stage artifact.
func writeRun(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	m := engine.Manifest{RunID: "r1", GraphName: "g", Goal: "fix", StartedAt: time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)}
	data, _ := json.Marshal(m)
	must(t, os.WriteFile(filepath.Join(dir, "run.json"), data, 0o644))
	events := []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1, RunID: "r1"},
		{Kind: engine.EventStageStarted, Seq: 2, RunID: "r1", NodeID: "work", Visit: 1, Attempt: 1},
		{Kind: engine.EventStageCompleted, Seq: 3, RunID: "r1", NodeID: "work", Visit: 1, Attempt: 1, Status: "success"},
		{Kind: engine.EventPipelineCompleted, Seq: 4, RunID: "r1", Status: "success"},
	}
	f, err := os.Create(filepath.Join(dir, "events.jsonl"))
	must(t, err)
	enc := json.NewEncoder(f)
	for _, ev := range events {
		must(t, enc.Encode(ev))
	}
	must(t, f.Close())
	must(t, os.MkdirAll(filepath.Join(dir, "work"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "work", "response.md"), []byte("did the thing"), 0o644))
	return dir
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Host = "127.0.0.1" // an allowed Host for the tokenless browser guard
	rr := httptest.NewRecorder()
	s.Handler(true).ServeHTTP(rr, req)
	return rr
}

// The single-run server keeps the daemon's /pipelines/{id}/... path
// shapes (D4: hub and run speak one schema); {id} must match the run.
func TestServer_EnrichedPipelineDoc(t *testing.T) {
	s := New(writeRun(t))
	rr := get(t, s, "/pipelines/r1")
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var doc runview.RunDoc
	must(t, json.Unmarshal(rr.Body.Bytes(), &doc))
	if doc.RunID != "r1" || doc.Status != "completed" || len(doc.Spans) != 1 {
		t.Fatalf("doc wrong: %+v", doc)
	}
	if doc.LastSeq != 4 {
		t.Fatalf("last_seq = %d, want 4", doc.LastSeq)
	}
}

func TestServer_UnknownRunIs404(t *testing.T) {
	s := New(writeRun(t))
	if rr := get(t, s, "/pipelines/nope"); rr.Code != 404 {
		t.Fatalf("status %d, want 404", rr.Code)
	}
}

// /pipelines lists the one run (same shape as the daemon's list: an
// array of summaries).
func TestServer_ListsSelf(t *testing.T) {
	s := New(writeRun(t))
	rr := get(t, s, "/pipelines")
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "r1") {
		t.Fatalf("list missing run: %s", rr.Body.String())
	}
}

// /events replays the log; ?since=N returns only events with Seq > N.
func TestServer_EventsSinceCursor(t *testing.T) {
	s := New(writeRun(t))
	rr := get(t, s, "/pipelines/r1/events?since=2")
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	lines := strings.Split(strings.TrimSpace(rr.Body.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d events, want 2 (seq 3,4): %v", len(lines), lines)
	}
	var ev engine.Event
	must(t, json.Unmarshal([]byte(lines[0]), &ev))
	if ev.Seq != 3 {
		t.Fatalf("first event seq = %d, want 3", ev.Seq)
	}
}

// /artifacts serves the run dir tree read-only.
func TestServer_Artifacts(t *testing.T) {
	s := New(writeRun(t))
	rr := get(t, s, "/pipelines/r1/artifacts/work/response.md")
	if rr.Code != 200 || rr.Body.String() != "did the thing" {
		t.Fatalf("artifact serve wrong: %d %q", rr.Code, rr.Body.String())
	}
	// Traversal out of the run dir must not escape.
	if rr := get(t, s, "/pipelines/r1/artifacts/../../../etc/passwd"); rr.Code == 200 {
		t.Fatal("path traversal escaped the run dir")
	}
	// Artifacts are served inert so an agent-controlled .html cannot run as
	// active content under the UI origin (and POST /answer same-origin).
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("artifact X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rr.Header().Get("Content-Security-Policy"); got != "sandbox" {
		t.Fatalf("artifact Content-Security-Policy = %q, want sandbox", got)
	}
}

// The dropped endpoints (D4) are gone.
func TestServer_DroppedEndpointsAbsent(t *testing.T) {
	s := New(writeRun(t))
	if rr := get(t, s, "/pipelines/r1/state"); rr.Code != 404 {
		t.Fatalf("/state should be 404, got %d", rr.Code)
	}
	if rr := get(t, s, "/pipelines/r1/nodes/work"); rr.Code != 404 {
		t.Fatalf("/nodes should be 404, got %d", rr.Code)
	}
}

// The graph endpoint renders the run's executed topology (graph.dot,
// written at run start) as SVG, so the UI can show what routes where.
func TestServer_GraphSVG(t *testing.T) {
	dir := writeRun(t)
	must(t, os.WriteFile(filepath.Join(dir, "graph.dot"), []byte(`digraph g {
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`), 0o644))
	s := New(dir)
	rr := get(t, s, "/pipelines/r1/graph")
	if rr.Code != 200 {
		t.Fatalf("graph status %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "svg") {
		t.Fatalf("content type %q, want svg", ct)
	}
	if !strings.Contains(rr.Body.String(), "<svg") {
		t.Fatalf("not svg: %.120s", rr.Body.String())
	}
	// A run dir without graph.dot 404s rather than erroring.
	s2 := New(writeRun(t))
	if rr := get(t, s2, "/pipelines/r1/graph"); rr.Code != 404 {
		t.Fatalf("missing graph.dot should 404, got %d", rr.Code)
	}
}
