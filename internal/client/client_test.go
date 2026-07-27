package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestClient points a Client at a test server.
func newTestClient(srv *httptest.Server) *Client {
	return &Client{BaseURL: srv.URL, HTTP: srv.Client()}
}

func TestListRuns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/pipelines" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"pipelines":[
			{"id":"abc","status":"running","graph_name":"triage","cwd":"/repo","started_at":"2026-07-27T02:00:00Z","events":3},
			{"id":"def","status":"completed","started_at":"2026-07-27T01:00:00Z","completed_at":"2026-07-27T01:05:00Z","outcome":"success","tokens":{"input_tokens":10,"output_tokens":20}}
		]}`))
	}))
	defer srv.Close()

	runs, err := newTestClient(srv).ListRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("want 2 runs, got %d", len(runs))
	}
	if runs[0].ID != "abc" || runs[0].Status != "running" || runs[0].GraphName != "triage" {
		t.Errorf("run[0] fields wrong: %+v", runs[0])
	}
	if runs[0].Events != 3 || runs[0].Cwd != "/repo" {
		t.Errorf("run[0] events/cwd wrong: %+v", runs[0])
	}
	if !runs[0].StartedAt.Equal(time.Date(2026, 7, 27, 2, 0, 0, 0, time.UTC)) {
		t.Errorf("run[0] started_at wrong: %v", runs[0].StartedAt)
	}
	if runs[1].Outcome != "success" || runs[1].Tokens == nil || runs[1].Tokens.OutputTokens != 20 {
		t.Errorf("run[1] outcome/tokens wrong: %+v", runs[1])
	}
	if runs[1].CompletedAt.IsZero() {
		t.Errorf("run[1] completed_at should be set")
	}
}

func TestGetRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/pipelines/xyz" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{"id":"xyz","status":"failed","failure_reason":"boom"}`))
	}))
	defer srv.Close()

	run, err := newTestClient(srv).GetRun(context.Background(), "xyz")
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != "xyz" || run.Status != "failed" || run.FailureReason != "boom" {
		t.Errorf("run fields wrong: %+v", run)
	}
}

func TestGetRunNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	if _, err := newTestClient(srv).GetRun(context.Background(), "missing"); err == nil {
		t.Fatal("want error for 404, got nil")
	}
}

// TestBearerToken asserts the client sends Authorization when Token is set,
// and omits it otherwise.
func TestBearerToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{"pipelines":[]}`))
	}))
	defer srv.Close()

	c := newTestClient(srv)
	c.Token = "s3cret"
	if _, err := c.ListRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer s3cret" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer s3cret")
	}

	c.Token = ""
	if _, err := c.ListRuns(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty when no token", gotAuth)
	}
}
