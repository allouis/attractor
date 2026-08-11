package server

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/interviewer"
)

// buildTestGraph parses a DOT source into a graph for state/node derivation
// tests. It fails the test on a parse/build error.
func buildTestGraph(t *testing.T, src string) *graph.Graph {
	t.Helper()
	file, err := dot.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g, err := graph.Build(file)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return g
}

// stateFixtureDOT is a small work-shaped pipeline: a fail edge routes test
// off to fix, which loops back, plus a pending open_pr terminal.
const stateFixtureDOT = `digraph implement {
  goal="ship it";
  start [shape=Mdiamond];
  plan;
  test;
  fix;
  open_pr;
  exit [shape=Msquare];
  start -> plan -> test;
  test -> fix [condition="outcome == 'fail'", label="on fail"];
  test -> open_pr [label="green"];
  fix -> test;
  open_pr -> exit;
}`

// stateDoc mirrors the /state response for assertions.
type stateDoc struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Repo         string `json:"repo"`
	WorkflowName string `json:"workflow_name"`
	Item         *struct {
		Identifier string `json:"identifier"`
		Title      string `json:"title"`
		URL        string `json:"url"`
		Source     string `json:"source"`
	} `json:"item"`
	Placement struct {
		Runner string `json:"runner"`
		Image  string `json:"image"`
	} `json:"placement"`
	StartedAt   string   `json:"started_at"`
	CompletedAt string   `json:"completed_at"`
	ActiveNodes []string `json:"active_nodes"`
	Nodes       map[string]struct {
		Status      string `json:"status"`
		Attempts    int    `json:"attempts"`
		StartedAt   string `json:"started_at"`
		CompletedAt string `json:"completed_at"`
		Outcome     string `json:"outcome"`
	} `json:"nodes"`
	Questions []map[string]any `json:"questions"`
}

func getState(t *testing.T, srv *Server, id string) (*http.Response, stateDoc) {
	t.Helper()
	resp, err := http.Get(srv.URL() + "/pipelines/" + id + "/state")
	if err != nil {
		t.Fatal(err)
	}
	var doc stateDoc
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
			resp.Body.Close()
			t.Fatalf("decode: %v", err)
		}
	}
	return resp, doc
}

func TestStateHeaderFields(t *testing.T) {
	srv, _ := newStageTestServer(t)
	g := buildTestGraph(t, stateFixtureDOT)
	t0 := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	run := &Run{
		ID:           "r1",
		status:       RunRunning,
		startedAt:    t0,
		graph:        g,
		repo:         "TryGhost/Ghost",
		workflowName: "implement",
		itemRef:      "linear:issue:abc123",
		placement:    "vm",
		image:        "default",
		initialContext: map[string]string{
			"identifier": "HKG-1914",
			"title":      "Fix the thing",
			"url":        "https://linear.app/x/HKG-1914",
		},
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
	}
	srv.registry.mu.Lock()
	srv.registry.runs["r1"] = run
	srv.registry.mu.Unlock()

	resp, doc := getState(t, srv, "r1")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if doc.ID != "r1" || doc.Status != "running" {
		t.Errorf("id/status = %q/%q", doc.ID, doc.Status)
	}
	if doc.Repo != "TryGhost/Ghost" || doc.WorkflowName != "implement" {
		t.Errorf("repo/workflow = %q/%q", doc.Repo, doc.WorkflowName)
	}
	if doc.Placement.Runner != "vm" || doc.Placement.Image != "default" {
		t.Errorf("placement = %+v", doc.Placement)
	}
	if doc.Item == nil {
		t.Fatal("item missing")
	}
	if doc.Item.Identifier != "HKG-1914" || doc.Item.Source != "linear" {
		t.Errorf("item identifier/source = %q/%q", doc.Item.Identifier, doc.Item.Source)
	}
	if doc.Item.Title != "Fix the thing" || doc.Item.URL != "https://linear.app/x/HKG-1914" {
		t.Errorf("item title/url = %q/%q", doc.Item.Title, doc.Item.URL)
	}
	if doc.StartedAt == "" {
		t.Error("started_at empty")
	}
}

// TestStateIdentifierPrefersVarOnReload pins the reload path: a run reloaded
// from disk after a daemon restart must still surface the human identifier from
// its seeded vars (e.g. "HKG-1914"), not the item_ref external id (the Linear
// UUID). That requires the seeded context to survive in the manifest.
func TestStateIdentifierPrefersVarOnReload(t *testing.T) {
	base := t.TempDir()
	writeManifestDir(t, base, "run1", Manifest{
		ID:      "run1",
		Status:  RunCompleted,
		ItemRef: "linear:issue:uuid-abc-123",
		InitialContext: map[string]string{
			"identifier": "HKG-1914",
			"title":      "Spike the jobs backend",
			"url":        "https://linear.app/x/HKG-1914",
		},
	})
	reg := newRunRegistry(base)
	run, ok := reg.Get("run1")
	if !ok {
		t.Fatal("run not reloaded")
	}
	st := run.State()
	if st.Item == nil {
		t.Fatal("item missing")
	}
	if st.Item.Identifier != "HKG-1914" {
		t.Errorf("identifier = %q, want HKG-1914 (seeded var), not the external id", st.Item.Identifier)
	}
	if st.Item.Source != "linear" {
		t.Errorf("source = %q, want linear", st.Item.Source)
	}
	if st.Item.Title != "Spike the jobs backend" {
		t.Errorf("title = %q", st.Item.Title)
	}
	if st.Item.URL != "https://linear.app/x/HKG-1914" {
		t.Errorf("url = %q", st.Item.URL)
	}
}

func TestStateDirectRunnerReportsDirect(t *testing.T) {
	srv, _ := newStageTestServer(t)
	run := &Run{
		ID:          "r1",
		status:      RunRunning,
		graph:       buildTestGraph(t, stateFixtureDOT),
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
	}
	srv.registry.mu.Lock()
	srv.registry.runs["r1"] = run
	srv.registry.mu.Unlock()
	resp, doc := getState(t, srv, "r1")
	defer resp.Body.Close()
	if doc.Placement.Runner != "direct" {
		t.Errorf("runner = %q, want direct", doc.Placement.Runner)
	}
}

// TestStateActiveNodesFailAware is the crux: a stage that reached a fail
// terminal and routed elsewhere must NOT show active, even though a naive
// started−completed set (completed counts only stage_completed) would keep
// it active. The node the run routed to is the sole active node.
func TestStateActiveNodesFailAware(t *testing.T) {
	srv, _ := newStageTestServer(t)
	g := buildTestGraph(t, stateFixtureDOT)
	t0 := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	ev := func(k engine.EventKind, node string, attempt int, status string, off int) engine.Event {
		return engine.Event{Kind: k, NodeID: node, Attempt: attempt, Status: status, Timestamp: t0.Add(time.Duration(off) * time.Second)}
	}
	history := []engine.Event{
		ev(engine.EventStageStarted, "plan", 1, "", 0),
		ev(engine.EventStageCompleted, "plan", 1, "success", 1),
		ev(engine.EventStageStarted, "test", 1, "", 2),
		ev(engine.EventStageFailed, "test", 1, "fail", 3),
		ev(engine.EventStageStarted, "fix", 1, "", 4),
	}
	run := &Run{
		ID:          "r1",
		status:      RunRunning,
		startedAt:   t0,
		graph:       g,
		history:     history,
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
	}
	srv.registry.mu.Lock()
	srv.registry.runs["r1"] = run
	srv.registry.mu.Unlock()

	resp, doc := getState(t, srv, "r1")
	defer resp.Body.Close()

	if len(doc.ActiveNodes) != 1 || doc.ActiveNodes[0] != "fix" {
		t.Errorf("active_nodes = %v, want [fix]", doc.ActiveNodes)
	}
	if n := doc.Nodes["plan"]; n.Status != "completed" || n.Outcome != "success" || n.Attempts != 1 {
		t.Errorf("plan = %+v", n)
	}
	if n := doc.Nodes["test"]; n.Status != "failed" || n.Attempts != 1 {
		t.Errorf("test = %+v, want failed/1", n)
	}
	if n := doc.Nodes["fix"]; n.Status != "running" || n.Attempts != 1 {
		t.Errorf("fix = %+v, want running/1", n)
	}
	// Every graph node present, unstarted ones pending.
	if n := doc.Nodes["open_pr"]; n.Status != "pending" {
		t.Errorf("open_pr = %+v, want pending", n)
	}
	if _, ok := doc.Nodes["exit"]; !ok {
		t.Error("exit node missing from nodes map")
	}
}

// TestStateAttemptsCountRetries: an in-node retry re-emits stage_started, so
// attempts equals the started count.
func TestStateAttemptsCountRetries(t *testing.T) {
	srv, _ := newStageTestServer(t)
	g := buildTestGraph(t, stateFixtureDOT)
	t0 := time.Now().UTC()
	ev := func(k engine.EventKind, node string, attempt int, status string) engine.Event {
		return engine.Event{Kind: k, NodeID: node, Attempt: attempt, Status: status, Timestamp: t0}
	}
	history := []engine.Event{
		ev(engine.EventStageStarted, "test", 1, ""),
		ev(engine.EventStageRetrying, "test", 1, ""),
		ev(engine.EventStageStarted, "test", 2, ""),
		ev(engine.EventStageCompleted, "test", 2, "success"),
	}
	run := &Run{
		ID: "r1", status: RunRunning, startedAt: t0, graph: g, history: history,
		subscribers: map[chan engine.Event]struct{}{}, questions: map[string]*pendingQuestion{},
	}
	srv.registry.mu.Lock()
	srv.registry.runs["r1"] = run
	srv.registry.mu.Unlock()
	resp, doc := getState(t, srv, "r1")
	defer resp.Body.Close()
	if n := doc.Nodes["test"]; n.Attempts != 2 || n.Status != "completed" || n.Outcome != "success" {
		t.Errorf("test = %+v, want attempts 2 completed success", n)
	}
	if len(doc.ActiveNodes) != 0 {
		t.Errorf("active_nodes = %v, want empty", doc.ActiveNodes)
	}
}

func TestStateIncludesPendingQuestion(t *testing.T) {
	srv, _ := newStageTestServer(t)
	run := &Run{
		ID: "r1", status: RunRunning, graph: buildTestGraph(t, stateFixtureDOT),
		subscribers: map[chan engine.Event]struct{}{},
		questions: map[string]*pendingQuestion{
			"q1": {question: interviewer.Question{Text: "proceed?"}, nodeID: "plan"},
		},
	}
	srv.registry.mu.Lock()
	srv.registry.runs["r1"] = run
	srv.registry.mu.Unlock()
	resp, doc := getState(t, srv, "r1")
	defer resp.Body.Close()
	if len(doc.Questions) != 1 {
		t.Fatalf("questions = %v, want 1", doc.Questions)
	}
	if doc.Questions[0]["id"] != "q1" {
		t.Errorf("question id = %v", doc.Questions[0]["id"])
	}
}

func TestStateUnknownRun404(t *testing.T) {
	srv, _ := newStageTestServer(t)
	resp, err := http.Get(srv.URL() + "/pipelines/missing/state")
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
