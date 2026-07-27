package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetStage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/pipelines/r1/stages/plan" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"status":"success",
			"prompt":"do the plan",
			"response":"planned it",
			"tool_calls":[{"hook_name":"post_tool","n":1},{"n":2}]
		}`))
	}))
	defer srv.Close()

	got, err := newTestClient(srv).GetStage(context.Background(), "r1", "plan")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "success" || got.Prompt != "do the plan" || got.Response != "planned it" {
		t.Errorf("fields wrong: %+v", got)
	}
	if len(got.ToolCalls) != 2 {
		t.Fatalf("tool_calls len = %d, want 2", len(got.ToolCalls))
	}
	var first struct {
		HookName string `json:"hook_name"`
	}
	if err := json.Unmarshal(got.ToolCalls[0], &first); err != nil {
		t.Fatal(err)
	}
	if first.HookName != "post_tool" {
		t.Errorf("raw tool call not preserved: %s", got.ToolCalls[0])
	}
}

func TestGetStageError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	if _, err := newTestClient(srv).GetStage(context.Background(), "r1", "nope"); err == nil {
		t.Fatal("want error for 404")
	}
}
