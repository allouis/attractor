package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/render"
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
	lns, _, err := serveView(dir, "127.0.0.1:0", false, "", true, nil, &buf)
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

func TestViewValidatesArg(t *testing.T) {
	// No directory argument.
	if err := View(nil); err == nil {
		t.Fatal("View with no arg: want error")
	}
	// A path that does not exist.
	if err := View([]string{filepath.Join(t.TempDir(), "nope")}); err == nil {
		t.Fatal("View on missing path: want error")
	}
	// A file, not a directory.
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := View([]string{f}); err == nil {
		t.Fatal("View on a file: want error")
	}
}

func TestViewMetaMissingGraphDot(t *testing.T) {
	// Missing graph.dot → nil, no panic (UI degrades gracefully).
	if m := viewMeta(t.TempDir()); m != nil {
		t.Fatalf("missing graph.dot: got %v, want nil", m)
	}
}

// The per-node metadata a re-served run reconstructs from the persisted
// graph.dot must equal what the live run built in-memory — same types
// (including tool / parallel.fan_in) and the same llm_model / thread_id.
func TestViewMetaRoundTripsLiveMetadata(t *testing.T) {
	src := `digraph d {
		start [shape=Mdiamond]
		build [type="codergen.acp", llm_model="claude-opus", thread_id="t1"]
		lint  [type="tool", tool_command="echo hi"]
		join  [type="parallel.fan_in"]
		done  [shape=Msquare]
		start -> build
		build -> lint
		lint -> join
		join -> done
	}`
	file, err := dot.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g, err := graph.Build(file)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := nodeMeta(g) // what a live run --ui serves

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "graph.dot"), render.TopologyDOT(g), 0o644); err != nil {
		t.Fatal(err)
	}
	got := viewMeta(dir)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("view metadata != live metadata:\n got  %+v\n want %+v", got, want)
	}
	// Spot-check the attributes shape alone cannot carry: the codergen
	// subtype (every codergen* shares shape=box) and model/thread.
	if got["build"].Type != "codergen.acp" {
		t.Errorf("build type = %q, want codergen.acp (explicit type must round-trip)", got["build"].Type)
	}
	if got["build"].Model != "claude-opus" || got["build"].ThreadID != "t1" {
		t.Errorf("build meta lost model/thread: %+v", got["build"])
	}
	if got["lint"].Type != "tool" {
		t.Errorf("lint type = %q, want tool", got["lint"].Type)
	}
	if got["join"].Type != "parallel.fan_in" {
		t.Errorf("join type = %q, want parallel.fan_in", got["join"].Type)
	}
}

// --no-tailnet (addTailnet=false) binds loopback only, even when a tailnet
// IP is present, so a sensitive run stays local.
func TestServeViewNoTailnetBindsLoopbackOnly(t *testing.T) {
	dir, _ := writeFinishedRun(t)
	var buf bytes.Buffer
	// A loopback IP stands in for a tailnet address so the (suppressed)
	// second bind would be bindable in CI if addTailnet were true.
	lns, _, err := serveView(dir, "127.0.0.1:0", false, "", false, ips("127.0.0.1"), &buf)
	if err != nil {
		t.Fatalf("serveView: %v", err)
	}
	defer closeAllLns(lns)
	if len(lns) != 1 {
		t.Fatalf("got %d listeners, want 1 (loopback only with --no-tailnet)", len(lns))
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
