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

// UploadStageDir uploads just one stage's directory incrementally (at
// stage_completed), keying each file by its path relative to the run root —
// the same key the terminal UploadDir sweep uses — so a completed stage's
// files reach the daemon while the run is still live. Nested child-pipeline
// dirs beneath the node are included; files outside the node dir are not.
func TestUploadStageDirKeysRelativeToRoot(t *testing.T) {
	var (
		mu  sync.Mutex
		got = map[string]string{}
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pipelines/{id}/artifacts/{path...}", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
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
	must("implement/response.md", "R")
	must("implement/status.json", "S")
	must("implement/child/status.json", "C") // nested manager_loop child
	must("plan/status.json", "P")            // a different stage
	must("events.jsonl", "E")                // daemon-owned, run root

	c := New(srv.URL, "run1", "tok")
	if err := c.UploadStageDir(root, "implement", nil); err != nil {
		t.Fatalf("UploadStageDir: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := map[string]string{
		"implement/response.md":       "R",
		"implement/status.json":       "S",
		"implement/child/status.json": "C",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("uploaded[%q] = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if _, ok := got["plan/status.json"]; ok {
		t.Fatalf("uploaded a file outside the requested stage dir")
	}
	if _, ok := got["events.jsonl"]; ok {
		t.Fatalf("uploaded a run-root file outside the stage dir")
	}
}

// A stage that completed without writing any files (its dir doesn't exist)
// is not an error — the terminal sweep still covers it if it appears later.
func TestUploadStageDirMissingDirIsNoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := New(srv.URL, "run1", "tok")
	if err := c.UploadStageDir(t.TempDir(), "never-ran", nil); err != nil {
		t.Fatalf("UploadStageDir(missing) = %v, want nil", err)
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
