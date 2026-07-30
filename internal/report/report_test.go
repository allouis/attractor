package report

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/allouis/attractor/internal/engine"
)

func TestClientEventPostsWithToken(t *testing.T) {
	var (
		mu    sync.Mutex
		got   []engine.Event
		auth  string
		gotID string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pipelines/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auth = r.Header.Get("Authorization")
		gotID = r.PathValue("id")
		var ev engine.Event
		_ = json.NewDecoder(r.Body).Decode(&ev)
		got = append(got, ev)
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "run123", "tok-abc")
	if err := c.Event(engine.Event{Kind: engine.EventStageStarted, NodeID: "plan"}); err != nil {
		t.Fatalf("Event: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].NodeID != "plan" {
		t.Fatalf("server got %+v", got)
	}
	if auth != "Bearer tok-abc" {
		t.Fatalf("auth = %q", auth)
	}
	if gotID != "run123" {
		t.Fatalf("id = %q", gotID)
	}
}

func TestUploadDirUploadsFilesWithRelPaths(t *testing.T) {
	var (
		mu   sync.Mutex
		got  = map[string]string{}
		auth string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pipelines/{id}/artifacts/{path...}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		auth = r.Header.Get("Authorization")
		got[r.PathValue("path")] = string(body)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	root := t.TempDir()
	must := func(p, c string) {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("plan/response.md", "R")
	must("plan/status.json", "S")
	must("events.jsonl", "E")

	c := New(srv.URL, "run1", "tok")
	skip := func(rel string) bool { return rel == "events.jsonl" }
	if err := c.UploadDir(root, skip); err != nil {
		t.Fatalf("UploadDir: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got["plan/response.md"] != "R" || got["plan/status.json"] != "S" {
		t.Fatalf("uploaded = %v", got)
	}
	if _, ok := got["events.jsonl"]; ok {
		t.Fatalf("events.jsonl should have been skipped")
	}
	if auth != "Bearer tok" {
		t.Fatalf("auth = %q", auth)
	}
}

func TestForwardDrainsChannel(t *testing.T) {
	var mu sync.Mutex
	var n int
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pipelines/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "r", "t")
	ch := make(chan engine.Event, 3)
	ch <- engine.Event{Kind: engine.EventPipelineStarted}
	ch <- engine.Event{Kind: engine.EventStageStarted}
	ch <- engine.Event{Kind: engine.EventPipelineCompleted}
	close(ch)
	c.Forward(ch)

	mu.Lock()
	defer mu.Unlock()
	if n != 3 {
		t.Fatalf("forwarded %d events, want 3", n)
	}
}
