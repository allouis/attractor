package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
)

// nodeDoc mirrors the /nodes/{node} response for assertions.
type nodeDoc struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Prompt  string `json:"prompt"`
	Timeout string `json:"timeout"`
	Retry   *struct {
		MaxRetries int `json:"max_retries"`
	} `json:"retry"`
	LLMProvider string `json:"llm_provider"`
	LLMModel    string `json:"llm_model"`
	Incoming    []struct {
		From      string `json:"from"`
		To        string `json:"to"`
		Condition string `json:"condition"`
		Label     string `json:"label"`
	} `json:"incoming"`
	Outgoing []struct {
		From      string `json:"from"`
		To        string `json:"to"`
		Condition string `json:"condition"`
		Label     string `json:"label"`
	} `json:"outgoing"`
}

func getNode(t *testing.T, srv *Server, id, node string) (*http.Response, nodeDoc) {
	t.Helper()
	resp, err := http.Get(srv.URL() + "/pipelines/" + id + "/nodes/" + node)
	if err != nil {
		t.Fatal(err)
	}
	var doc nodeDoc
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
			resp.Body.Close()
			t.Fatalf("decode: %v", err)
		}
	}
	return resp, doc
}

func addGraphRun(srv *Server, id string, g *graph.Graph) *Run {
	run := &Run{
		ID: id, status: RunRunning, graph: g,
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
	}
	srv.registry.mu.Lock()
	srv.registry.runs[id] = run
	srv.registry.mu.Unlock()
	return run
}

const nodeFixtureDOT = `digraph implement {
  start [shape=Mdiamond];
  plan [prompt="draft a plan", timeout="5m", max_retries="3", llm_provider="anthropic", llm_model="claude-opus-4"];
  test;
  fix;
  open_pr;
  start -> plan -> test;
  test -> fix [condition="outcome == 'fail'", label="on fail"];
  test -> open_pr [label="green"];
  fix -> test;
}`

// TestNodeServesGraphDetail: a node's type, prompt, timeout, retry, llm attrs,
// and in/out edges (with conditions + labels) all come from the graph and are
// served for an unstarted node from run start.
func TestNodeServesGraphDetail(t *testing.T) {
	srv, _ := newStageTestServer(t)
	addGraphRun(srv, "r1", buildTestGraph(t, nodeFixtureDOT))

	resp, doc := getNode(t, srv, "r1", "plan")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if doc.ID != "plan" || doc.Type != "codergen" {
		t.Errorf("id/type = %q/%q", doc.ID, doc.Type)
	}
	if doc.Prompt != "draft a plan" {
		t.Errorf("prompt = %q", doc.Prompt)
	}
	if doc.Timeout != "5m" {
		t.Errorf("timeout = %q", doc.Timeout)
	}
	if doc.Retry == nil || doc.Retry.MaxRetries != 3 {
		t.Errorf("retry = %+v, want max_retries 3", doc.Retry)
	}
	if doc.LLMProvider != "anthropic" || doc.LLMModel != "claude-opus-4" {
		t.Errorf("llm = %q/%q", doc.LLMProvider, doc.LLMModel)
	}
	if len(doc.Incoming) != 1 || doc.Incoming[0].From != "start" {
		t.Errorf("incoming = %+v, want [start->plan]", doc.Incoming)
	}
	if len(doc.Outgoing) != 1 || doc.Outgoing[0].To != "test" {
		t.Errorf("outgoing = %+v, want [plan->test]", doc.Outgoing)
	}
}

// TestNodeEdgeConditionsAndLabels: the branching test node exposes both
// outgoing edges with their conditions and labels.
func TestNodeEdgeConditionsAndLabels(t *testing.T) {
	srv, _ := newStageTestServer(t)
	addGraphRun(srv, "r1", buildTestGraph(t, nodeFixtureDOT))

	resp, doc := getNode(t, srv, "r1", "test")
	defer resp.Body.Close()
	if len(doc.Outgoing) != 2 {
		t.Fatalf("outgoing = %+v, want 2", doc.Outgoing)
	}
	byTo := map[string]struct {
		From, To, Condition, Label string
	}{}
	for _, e := range doc.Outgoing {
		byTo[e.To] = struct{ From, To, Condition, Label string }{e.From, e.To, e.Condition, e.Label}
	}
	if e := byTo["fix"]; e.Condition != "outcome == 'fail'" || e.Label != "on fail" {
		t.Errorf("test->fix = %+v", e)
	}
	if e := byTo["open_pr"]; e.Label != "green" {
		t.Errorf("test->open_pr = %+v", e)
	}
}

// TestNodeResolvedPromptFromSource: a run reloaded from disk (no in-memory
// graph) still serves the @file-resolved prompt by re-preparing the stored
// source against the run's base dir.
func TestNodeResolvedPromptFromSource(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	baseDir := filepath.Join(tmp, "wf")
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "plan.md"), []byte("resolved plan body"), 0o644); err != nil {
		t.Fatal(err)
	}
	logsRoot := filepath.Join(tmp, "r1")
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `digraph implement {
  goal="ship it";
  start [shape=Mdiamond];
  plan [prompt="@plan.md"];
  exit [shape=Msquare];
  start -> plan -> exit;
}`
	if err := os.WriteFile(filepath.Join(logsRoot, "source.dot"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// A reloaded run: graph nil, source only on disk, cwd is the base dir.
	run := &Run{
		ID: "r1", status: RunCancelled, logsRoot: logsRoot, cwd: baseDir,
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
	}
	srv.registry.mu.Lock()
	srv.registry.runs["r1"] = run
	srv.registry.mu.Unlock()

	resp, doc := getNode(t, srv, "r1", "plan")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if doc.Prompt != "resolved plan body" {
		t.Errorf("prompt = %q, want resolved plan body", doc.Prompt)
	}
}

// TestNodeResolvedPromptOnReloadDistinctBaseDir is the reload regression: a run
// whose @file prompts live in the workflow base dir — NOT the repo cwd — must
// still serve resolved prompt text after reload. The daemon holds no in-memory
// graph then; re-preparing against cwd (the repo, which has no prompts/) fails
// and the endpoint would serve the raw `@prompts/…` ref. The persisted source
// base dir is what makes the re-prepare resolve.
func TestNodeResolvedPromptOnReloadDistinctBaseDir(t *testing.T) {
	tmp := t.TempDir()
	wfDir := filepath.Join(tmp, "workflows", "implement")
	if err := os.MkdirAll(filepath.Join(wfDir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, "prompts", "plan.md"), []byte("resolved plan body"), 0o644); err != nil {
		t.Fatal(err)
	}
	repoDir := filepath.Join(tmp, "repo") // the run's cwd — has no prompts/
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A daemon logs root the registry reloads from.
	base := filepath.Join(tmp, "logs")
	logsRoot := filepath.Join(base, "r1")
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `digraph implement {
  start [shape=Mdiamond];
  plan [prompt="@prompts/plan.md"];
  exit [shape=Msquare];
  start -> plan -> exit;
}`
	if err := os.WriteFile(filepath.Join(logsRoot, "source.dot"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	writeManifestDir(t, base, "r1", Manifest{
		ID: "r1", Status: RunCompleted, LogsRoot: logsRoot,
		Cwd: repoDir, SourceBaseDir: wfDir,
	})

	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: base})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	resp, doc := getNode(t, srv, "r1", "plan")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if doc.Prompt != "resolved plan body" {
		t.Errorf("prompt = %q, want resolved plan body (not the raw @prompts ref)", doc.Prompt)
	}
}

func TestNodeUnknownRun404(t *testing.T) {
	srv, _ := newStageTestServer(t)
	resp, err := http.Get(srv.URL() + "/pipelines/missing/nodes/plan")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["error"]; !ok {
		t.Errorf("body = %v, want error field", body)
	}
}

func TestNodeUnknownNode404(t *testing.T) {
	srv, _ := newStageTestServer(t)
	addGraphRun(srv, "r1", buildTestGraph(t, nodeFixtureDOT))
	resp, err := http.Get(srv.URL() + "/pipelines/r1/nodes/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["error"]; !ok {
		t.Errorf("body = %v, want error field", body)
	}
}
