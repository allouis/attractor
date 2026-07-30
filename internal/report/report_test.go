package report

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
