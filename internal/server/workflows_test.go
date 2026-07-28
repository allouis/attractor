package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// workflowsServer starts a server whose workflow catalog root is dir.
func workflowsServer(t *testing.T, dir string) *Server {
	t.Helper()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir(), WorkflowsDir: dir})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// writeWorkflow creates <root>/<name>/pipeline.dot with a trivial graph.
func writeWorkflow(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `digraph ` + name + ` {
		start [shape=Mdiamond]
		a [prompt="x"]
		done [shape=Msquare]
		start -> a -> done
	}`
	if err := os.WriteFile(filepath.Join(dir, "pipeline.dot"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWorkflowsCatalog_ListsDirsWithPipelineDot proves GET /workflows lists
// each catalog subdir holding a pipeline.dot, name-sorted, with the absolute
// path to that file — and ignores dirs without one and stray loose files.
func TestWorkflowsCatalog_ListsDirsWithPipelineDot(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "beta")
	writeWorkflow(t, root, "alpha")
	if err := os.MkdirAll(filepath.Join(root, "nope"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "loose.dot"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := workflowsServer(t, root)

	resp, err := http.Get(srv.URL() + "/workflows")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type=%q", ct)
	}
	var got []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d workflows, want 2: %+v", len(got), got)
	}
	if got[0].Name != "alpha" || got[1].Name != "beta" {
		t.Fatalf("names=%q,%q want alpha,beta (sorted)", got[0].Name, got[1].Name)
	}
	wantPath := filepath.Join(root, "alpha", "pipeline.dot")
	if got[0].Path != wantPath {
		t.Fatalf("alpha path=%q, want %q", got[0].Path, wantPath)
	}
	if !filepath.IsAbs(got[0].Path) {
		t.Fatalf("path not absolute: %q", got[0].Path)
	}
}

// TestWorkflowsCatalog_MissingRootIsEmpty proves a nonexistent catalog root
// yields 200 with an empty JSON array, not null and not an error.
func TestWorkflowsCatalog_MissingRootIsEmpty(t *testing.T) {
	srv := workflowsServer(t, filepath.Join(t.TempDir(), "does-not-exist"))

	resp, err := http.Get(srv.URL() + "/workflows")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	body := make([]byte, 0)
	dec := json.NewDecoder(resp.Body)
	var got []any
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("decode: %v (body=%q)", err, body)
	}
	if got == nil {
		t.Fatal("want [] not null")
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}
