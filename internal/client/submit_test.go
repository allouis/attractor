package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSubmit(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/pipelines" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &gotBody)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":"newrun"}`))
	}))
	defer srv.Close()

	id, err := newTestClient(srv).Submit(context.Background(), SubmitRequest{
		Dot:  "digraph{}",
		Cwd:  "/repo",
		Vars: map[string]string{"name": "world"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "newrun" {
		t.Errorf("id = %q, want newrun", id)
	}
	if gotBody["dot"] != "digraph{}" || gotBody["cwd"] != "/repo" {
		t.Errorf("body dot/cwd wrong: %v", gotBody)
	}
	vars, ok := gotBody["vars"].(map[string]any)
	if !ok || vars["name"] != "world" {
		t.Errorf("body vars wrong: %v", gotBody["vars"])
	}
}

func TestSubmitRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "validate: bad graph", http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	if _, err := newTestClient(srv).Submit(context.Background(), SubmitRequest{Dot: "x"}); err == nil {
		t.Fatal("want error for 422, got nil")
	}
}

func TestCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/pipelines/abc/cancel" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"cancelled":true}`))
	}))
	defer srv.Close()

	if err := newTestClient(srv).Cancel(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
}
