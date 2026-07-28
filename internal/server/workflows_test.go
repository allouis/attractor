package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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

// TestWorkflowGraph_RendersSVG proves GET /workflows/{name}/graph renders the
// definition file to SVG via render.SVG, same as the run-graph endpoint.
func TestWorkflowGraph_RendersSVG(t *testing.T) {
	if _, err := exec.LookPath("dot"); err != nil {
		t.Skip("graphviz `dot` not on PATH")
	}
	root := t.TempDir()
	writeWorkflow(t, root, "alpha")
	srv := workflowsServer(t, root)

	resp, err := http.Get(srv.URL() + "/workflows/alpha/graph")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/svg+xml" {
		t.Fatalf("content-type=%q", ct)
	}
	if !bytes.Contains(body, []byte("<svg")) {
		t.Fatal("graph endpoint did not return SVG")
	}
}

// TestWorkflowGraph_UnknownName404 proves an absent definition is a 404, not
// a render error.
func TestWorkflowGraph_UnknownName404(t *testing.T) {
	srv := workflowsServer(t, t.TempDir())

	resp, err := http.Get(srv.URL() + "/workflows/ghost/graph")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

// TestWorkflowGraph_RejectsTraversal proves a name that escapes the catalog
// root is confined to a 404 and never reads outside it. The handler is
// exercised directly because net/http normalizes ".." out of the request
// path before it reaches the mux.
func TestWorkflowGraph_RejectsTraversal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "catalog")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// A pipeline.dot one level above the catalog root: a successful
	// traversal would render it.
	outside := filepath.Dir(root)
	if err := os.WriteFile(filepath.Join(outside, "pipeline.dot"), []byte("digraph x { a }"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{workflowsDir: root}

	req := httptest.NewRequest(http.MethodGet, "/workflows/x/graph", nil)
	req.SetPathValue("name", "..")
	rec := httptest.NewRecorder()
	s.getWorkflowGraph(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 (traversal must be rejected)", rec.Code)
	}
}
