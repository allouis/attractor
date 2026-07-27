package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// newStageTestServer starts a server with an empty registry and returns it
// plus its logs root. Tests write fake stage dirs under logsRoot/{id}/{node}
// and hit GET /pipelines/{id}/stages/{node}.
func newStageTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, tmp
}

// addRun inserts a bare Run into the registry with logsRoot under the server's
// base dir.
func addRun(srv *Server, id, logsRoot string) {
	srv.registry.mu.Lock()
	srv.registry.runs[id] = &Run{ID: id, logsRoot: logsRoot}
	srv.registry.mu.Unlock()
}

func TestGetStageReadsStageDir(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	logsRoot := filepath.Join(tmp, "r1")
	addRun(srv, "r1", logsRoot)

	stageDir := filepath.Join(logsRoot, "plan")
	if err := os.MkdirAll(filepath.Join(stageDir, "tool_calls"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(stageDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("prompt.md", "do the plan")
	write("response.md", "planned it")
	write("status.json", `{"outcome":"success","notes":"ok"}`)
	write(filepath.Join("tool_calls", "1-post_tool.json"), `{"hook_name":"post_tool","n":1}`)
	write(filepath.Join("tool_calls", "2-post_tool.json"), `{"hook_name":"post_tool","n":2}`)

	resp, err := http.Get(srv.URL() + "/pipelines/r1/stages/plan")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var got struct {
		Status    string            `json:"status"`
		Prompt    string            `json:"prompt"`
		Response  string            `json:"response"`
		ToolCalls []json.RawMessage `json:"tool_calls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "success" {
		t.Errorf("status = %q, want success", got.Status)
	}
	if got.Prompt != "do the plan" {
		t.Errorf("prompt = %q", got.Prompt)
	}
	if got.Response != "planned it" {
		t.Errorf("response = %q", got.Response)
	}
	if len(got.ToolCalls) != 2 {
		t.Fatalf("tool_calls len = %d, want 2", len(got.ToolCalls))
	}
	var first struct{ N int }
	if err := json.Unmarshal(got.ToolCalls[0], &first); err != nil {
		t.Fatal(err)
	}
	if first.N != 1 {
		t.Errorf("first tool call n = %d, want 1", first.N)
	}
}

// TestGetStageNumericToolCallOrder guards against lexical sorting placing
// "10-" before "2-".
func TestGetStageNumericToolCallOrder(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	logsRoot := filepath.Join(tmp, "r1")
	addRun(srv, "r1", logsRoot)
	toolDir := filepath.Join(logsRoot, "plan", "tool_calls")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 2, 10} {
		body := []byte(`{"n":` + strconv.Itoa(n) + `}`)
		if err := os.WriteFile(filepath.Join(toolDir, strconv.Itoa(n)+"-post_tool.json"), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := http.Get(srv.URL() + "/pipelines/r1/stages/plan")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got struct {
		ToolCalls []struct{ N int } `json:"tool_calls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := []int{1, 2, 10}
	if len(got.ToolCalls) != len(want) {
		t.Fatalf("len = %d, want %d", len(got.ToolCalls), len(want))
	}
	for i, w := range want {
		if got.ToolCalls[i].N != w {
			t.Errorf("tool_calls[%d].n = %d, want %d", i, got.ToolCalls[i].N, w)
		}
	}
}

func TestGetStageNoStatusFile(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	logsRoot := filepath.Join(tmp, "r1")
	addRun(srv, "r1", logsRoot)
	stageDir := filepath.Join(logsRoot, "plan")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "prompt.md"), []byte("p"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL() + "/pipelines/r1/stages/plan")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var got struct {
		Status    string            `json:"status"`
		ToolCalls []json.RawMessage `json:"tool_calls"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "" {
		t.Errorf("status = %q, want empty", got.Status)
	}
	if len(got.ToolCalls) != 0 {
		t.Errorf("tool_calls len = %d, want 0", len(got.ToolCalls))
	}
}

func TestGetStageUnknownStage404(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	addRun(srv, "r1", filepath.Join(tmp, "r1"))
	resp, err := http.Get(srv.URL() + "/pipelines/r1/stages/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

func TestGetStageUnknownRun404(t *testing.T) {
	srv, _ := newStageTestServer(t)
	resp, err := http.Get(srv.URL() + "/pipelines/missing/stages/plan")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}
