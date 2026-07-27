package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// writeSSE writes one event frame in the daemon's format: an event:
// line, a data: line, then a blank line.
func writeSSE(w http.ResponseWriter, f http.Flusher, kind, data string) {
	fmt.Fprintf(w, "event: %s\n", kind)
	fmt.Fprintf(w, "data: %s\n\n", data)
	f.Flush()
}

func drain(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var got []Event
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, ev)
		case <-timeout:
			t.Fatal("timed out waiting for stream to close")
		}
	}
}

func TestStreamEventsTerminalCloses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pipelines/abc/events" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		f.Flush()
		writeSSE(w, f, "stage_started", `{"kind":"stage_started","node_id":"plan"}`)
		writeSSE(w, f, "stage_completed", `{"kind":"stage_completed","node_id":"plan"}`)
		writeSSE(w, f, "pipeline_completed", `{"kind":"pipeline_completed","usage":{"input_tokens":5,"output_tokens":7}}`)
		// Keep the handler alive briefly; the client should close its
		// channel on the terminal event without needing the body to EOF.
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()

	ch, err := newTestClient(srv).StreamEvents(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)
	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "stage_started" || got[0].NodeID != "plan" {
		t.Errorf("event[0] wrong: %+v", got[0])
	}
	if got[2].Kind != "pipeline_completed" || got[2].Usage == nil || got[2].Usage.OutputTokens != 7 {
		t.Errorf("terminal event wrong: %+v", got[2])
	}
}

func TestStreamEventsContextCancelCloses(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		f.Flush()
		writeSSE(w, f, "stage_started", `{"kind":"stage_started","node_id":"plan"}`)
		<-release // never send a terminal event
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := newTestClient(srv).StreamEvents(ctx, "abc")
	if err != nil {
		t.Fatal(err)
	}
	// First event arrives, then we cancel; the channel must close.
	ev, ok := <-ch
	if !ok || ev.Kind != "stage_started" {
		t.Fatalf("want stage_started, got %+v ok=%v", ev, ok)
	}
	cancel()
	got := drain(t, ch) // should return promptly once channel closes
	_ = got
}

func TestStreamEventsEOFCloses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		f.Flush()
		writeSSE(w, f, "stage_started", `{"kind":"stage_started"}`)
		// Handler returns → body EOFs with no terminal event.
	}))
	defer srv.Close()

	ch, err := newTestClient(srv).StreamEvents(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)
	if len(got) != 1 || got[0].Kind != "stage_started" {
		t.Fatalf("want 1 stage_started event, got %+v", got)
	}
}

// TestStreamEventsSincePassesCursor checks the resume path: StreamEventsSince
// sends ?since=<seq> and delivers only the replayed tail without duplication
// (tui-spec T3 regression guard for the reconnect-dup bug).
func TestStreamEventsSincePassesCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("since"); got != "100" {
			t.Errorf("since query = %q, want 100", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		f.Flush()
		writeSSE(w, f, "stage_progress", `{"kind":"stage_progress","seq":101}`)
		writeSSE(w, f, "stage_completed", `{"kind":"stage_completed","seq":102}`)
		writeSSE(w, f, "pipeline_completed", `{"kind":"pipeline_completed","seq":103}`)
	}))
	defer srv.Close()

	ch, err := newTestClient(srv).StreamEventsSince(context.Background(), "abc", 100)
	if err != nil {
		t.Fatal(err)
	}
	got := drain(t, ch)
	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d: %+v", len(got), got)
	}
	if got[0].Seq != 101 || got[2].Seq != 103 {
		t.Errorf("seq mismatch: %+v", got)
	}
}

// TestStreamEventsOmitsSinceWhenZero verifies the default StreamEvents does not
// send a since query so it gets a full replay.
func TestStreamEventsOmitsSinceWhenZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("unexpected query %q, want none", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		f.Flush()
		writeSSE(w, f, "pipeline_completed", `{"kind":"pipeline_completed","seq":1}`)
	}))
	defer srv.Close()

	ch, err := newTestClient(srv).StreamEvents(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, ch); len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
}

func TestStreamEventsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	if _, err := newTestClient(srv).StreamEvents(context.Background(), "missing"); err == nil {
		t.Fatal("want error for 404, got nil")
	}
}
