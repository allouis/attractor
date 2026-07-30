package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/engine"
)

// A phone-home child reports engine events to the daemon over HTTP; the
// daemon ingests them into the run's history, SSE fan-out, and its own
// events.jsonl (the child's events.jsonl lives on the child's FS).
func TestIngestEventAppendsHistoryAndPersists(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	run := srv.registry.NewRun("digraph{}", nil, nil, tmp, nil, "", "", nil)

	ev := engine.Event{Seq: 1, Kind: engine.EventStageStarted, NodeID: "plan"}
	body, _ := json.Marshal(ev)
	req := httptest.NewRequest(http.MethodPost, "/pipelines/"+run.ID+"/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+run.Token())
	rec := httptest.NewRecorder()
	srv.httpsrv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	if got := run.Summary()["events"].(int); got != 1 {
		t.Fatalf("history len = %d, want 1", got)
	}
	data, err := os.ReadFile(filepath.Join(run.logsRoot, "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if !strings.Contains(string(data), "stage_started") {
		t.Fatalf("events.jsonl missing ingested event: %q", data)
	}
}

// The child polls GET /control to learn whether it should abort. After
// Cancel, the poll reports cancel=true so the child can stop its engine.
func TestControlReportsCancel(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	run := srv.registry.NewRun("digraph{}", nil, nil, tmp, nil, "", "", nil)

	poll := func() controlResponse {
		req := httptest.NewRequest(http.MethodGet, "/pipelines/"+run.ID+"/control", nil)
		req.Header.Set("Authorization", "Bearer "+run.Token())
		rec := httptest.NewRecorder()
		srv.httpsrv.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("control status = %d, want 200", rec.Code)
		}
		var got controlResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode control: %v", err)
		}
		return got
	}

	if poll().Cancel {
		t.Fatalf("cancel true before Cancel()")
	}
	run.Cancel()
	if !poll().Cancel {
		t.Fatalf("cancel false after Cancel()")
	}
}

// Control is token-gated like ingest.
func TestControlRejectsBadToken(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	run := srv.registry.NewRun("digraph{}", nil, nil, tmp, nil, "", "", nil)
	req := httptest.NewRequest(http.MethodGet, "/pipelines/"+run.ID+"/control", nil)
	req.Header.Set("Authorization", "Bearer nope")
	rec := httptest.NewRecorder()
	srv.httpsrv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// A phone-home child uploads its stage artifacts; the daemon writes them
// under the run's logs root so GET /artifacts and GET /stages serve them
// exactly as for an in-process run.
func TestUploadArtifactWritesUnderLogsRoot(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	run := srv.registry.NewRun("digraph{}", nil, nil, tmp, nil, "", "", nil)

	req := httptest.NewRequest(http.MethodPost, "/pipelines/"+run.ID+"/artifacts/plan/response.md", strings.NewReader("hello plan"))
	req.Header.Set("Authorization", "Bearer "+run.Token())
	rec := httptest.NewRecorder()
	srv.httpsrv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(run.logsRoot, "plan", "response.md"))
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(data) != "hello plan" {
		t.Fatalf("content = %q", data)
	}
}

func TestUploadArtifactRejectsTraversal(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	run := srv.registry.NewRun("digraph{}", nil, nil, tmp, nil, "", "", nil)

	req := httptest.NewRequest(http.MethodPost, "/pipelines/"+run.ID+"/artifacts/../../escape.txt", strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer "+run.Token())
	rec := httptest.NewRecorder()
	srv.httpsrv.Handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusNoContent {
		t.Fatalf("traversal upload accepted")
	}
	if _, err := os.Stat(filepath.Join(tmp, "escape.txt")); err == nil {
		t.Fatalf("traversal wrote outside logs root")
	}
}

// The run token gates ingest: a wrong (or missing) token is rejected so a
// stray process can't inject events into someone else's run.
func TestIngestEventRejectsBadToken(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	run := srv.registry.NewRun("digraph{}", nil, nil, tmp, nil, "", "", nil)

	ev := engine.Event{Seq: 1, Kind: engine.EventStageStarted, NodeID: "plan"}
	body, _ := json.Marshal(ev)
	req := httptest.NewRequest(http.MethodPost, "/pipelines/"+run.ID+"/events", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	srv.httpsrv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if got := run.Summary()["events"].(int); got != 0 {
		t.Fatalf("history len = %d, want 0 (rejected)", got)
	}
}
