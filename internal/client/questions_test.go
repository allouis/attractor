package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListQuestions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/pipelines/abc/questions" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{"questions":[
			{"id":"q1","node_id":"gate","text":"Proceed?","options":[{"key":"y","label":"Yes"},{"key":"n","label":"No"}]}
		]}`))
	}))
	defer srv.Close()

	qs, err := newTestClient(srv).ListQuestions(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 {
		t.Fatalf("want 1 question, got %d", len(qs))
	}
	q := qs[0]
	if q.ID != "q1" || q.NodeID != "gate" || q.Text != "Proceed?" {
		t.Errorf("question fields wrong: %+v", q)
	}
	if len(q.Options) != 2 || q.Options[0].Key != "y" || q.Options[1].Label != "No" {
		t.Errorf("options wrong: %+v", q.Options)
	}
}

func TestAnswer(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/pipelines/abc/questions/q1/answer" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &gotBody)
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"accepted":true}`))
	}))
	defer srv.Close()

	if err := newTestClient(srv).Answer(context.Background(), "abc", "q1", "y"); err != nil {
		t.Fatal(err)
	}
	if gotBody["key"] != "y" {
		t.Errorf("body key = %v, want y", gotBody["key"])
	}
}
