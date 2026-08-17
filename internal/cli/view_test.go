package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/engine"
)

// writeFinishedRun materializes a completed run dir: run.json, an
// events.jsonl ending in pipeline_completed, a graph.dot, and one
// artifact — everything `view` reads back off disk.
func writeFinishedRun(t *testing.T) (dir, id string) {
	t.Helper()
	dir = t.TempDir()
	id = "view-run-1"
	m := engine.Manifest{RunID: id, GraphName: "demo", StartedAt: time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "run.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	events := []engine.Event{
		{Kind: engine.EventPipelineStarted, Seq: 1, RunID: id},
		{Kind: engine.EventStageStarted, Seq: 2, RunID: id, NodeID: "work", Visit: 1, Attempt: 1},
		{Kind: engine.EventStageCompleted, Seq: 3, RunID: id, NodeID: "work", Visit: 1, Attempt: 1, Status: "success"},
		{Kind: engine.EventPipelineCompleted, Seq: 4, RunID: id, Status: "success"},
	}
	f, err := os.Create(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	if err := os.WriteFile(filepath.Join(dir, "graph.dot"), []byte("digraph demo {\n  start -> work -> exit\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "work", "response.md"), []byte("did the thing"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, id
}

func TestServeView_ServesFinishedRunDir(t *testing.T) {
	dir, id := writeFinishedRun(t)
	var buf bytes.Buffer
	lns, _, err := serveView(dir, "127.0.0.1:0", false, "", nil, &buf)
	if err != nil {
		t.Fatalf("serveView: %v", err)
	}
	defer closeAllLns(lns)
	if len(lns) != 1 {
		t.Fatalf("got %d listeners, want 1 (loopback only, no tailnet)", len(lns))
	}
	base := "http://" + lns[0].Addr().String()

	// UI serves HTML.
	resp, err := httpClient.Get(base + "/ui")
	if err != nil {
		t.Fatalf("GET /ui: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /ui: %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("GET /ui content-type %q, want text/html", ct)
	}

	// /pipelines lists the run.
	resp, err = httpClient.Get(base + "/pipelines")
	if err != nil {
		t.Fatalf("GET /pipelines: %v", err)
	}
	if !strings.Contains(readBody(t, resp), id) {
		t.Fatalf("GET /pipelines does not list run %q", id)
	}

	// /pipelines/<id> is the completed doc.
	resp, err = httpClient.Get(base + "/pipelines/" + id)
	if err != nil {
		t.Fatalf("GET /pipelines/%s: %v", id, err)
	}
	var doc struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(readBody(t, resp)), &doc); err != nil {
		t.Fatalf("decode doc: %v", err)
	}
	if doc.Status != "completed" {
		t.Fatalf("doc status %q, want completed", doc.Status)
	}

	// Read-only: no live interviewer, so answering a gate is 409.
	answerURL := base + "/pipelines/" + id + "/questions/x/answer"
	req, _ := http.NewRequest("POST", answerURL, strings.NewReader(`{"value":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	ansResp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("POST answer: %v", err)
	}
	ansResp.Body.Close()
	if ansResp.StatusCode != http.StatusConflict {
		t.Fatalf("POST answer: %d, want 409", ansResp.StatusCode)
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}
