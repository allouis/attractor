package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fabro/attractor/internal/config"
	"github.com/fabro/attractor/internal/engine"
	"github.com/fabro/attractor/internal/source"
)

// fakeSource is an injectable Source recording the filter it was called
// with and replaying canned items.
type fakeSource struct {
	items   []source.Item
	err     error
	filter  source.Filter
	getItem source.Item
	getErr  error
	gotRef  engine.ItemRef
}

func (f *fakeSource) List(_ context.Context, filter source.Filter) ([]source.Item, error) {
	f.filter = filter
	return f.items, f.err
}

func (f *fakeSource) Get(_ context.Context, ref engine.ItemRef) (source.Item, error) {
	f.gotRef = ref
	return f.getItem, f.getErr
}

func itemsServer(t *testing.T, sources map[string]source.Source) *Server {
	t.Helper()
	return itemsServerWithRepos(t, sources, nil)
}

func itemsServerWithRepos(t *testing.T, sources map[string]source.Source, repos config.Repos) *Server {
	t.Helper()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir(), Sources: sources, Repos: repos})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// A minimal start->done graph completes instantly with no codergen
// backend, so a dispatched run terminates deterministically.
const doneGraph = "digraph { start [shape=Mdiamond]; done [shape=Msquare]; start -> done }"

// writeGraph writes doneGraph to a temp .dot and returns its path.
func writeGraph(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pipe.dot")
	if err := os.WriteFile(p, []byte(doneGraph), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// postRunItem POSTs POST /items/run and returns the response plus decoded id.
func postRunItem(t *testing.T, url string, body map[string]any) (*http.Response, string) {
	t.Helper()
	buf, _ := json.Marshal(body)
	resp, err := http.Post(url+"/items/run", "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return resp, out.ID
}

// pollRunSummary fetches GET /pipelines/{id} until terminal, returning the
// last summary so cleanup races the writer goroutine to a stop.
func pollRunSummary(t *testing.T, url, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(url + "/pipelines/" + id)
		if err != nil {
			t.Fatal(err)
		}
		var summary map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&summary)
		resp.Body.Close()
		status, _ := summary["status"].(string)
		if status == string(RunCompleted) || status == string(RunFailed) || status == string(RunCancelled) {
			return summary
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s did not terminate; last status = %q", id, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func prItem() source.Item {
	return source.Item{
		Ref:   engine.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"},
		Title: "Fix login",
		Vars: map[string]string{
			"repo":      "allouis/attractor",
			"pr_number": "42",
		},
	}
}

func TestRunItemPRAutofillsRepo(t *testing.T) {
	repoDir := t.TempDir()
	fs := &fakeSource{getItem: prItem()}
	repos := config.Repos{"allouis/attractor": repoDir}
	srv := itemsServerWithRepos(t, map[string]source.Source{"github": fs}, repos)

	ref := engine.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"}
	resp, id := postRunItem(t, srv.URL(), map[string]any{
		"item_ref": ref,
		"pipeline": writeGraph(t),
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if fs.gotRef != ref {
		t.Errorf("source.Get ref = %+v, want %+v", fs.gotRef, ref)
	}
	summary := pollRunSummary(t, srv.URL(), id)
	if got, _ := summary["cwd"].(string); got != repoDir {
		t.Errorf("run cwd = %q, want %q (repo auto-filled from item)", got, repoDir)
	}
	if summary["item_ref"] == nil {
		t.Error("run summary missing item_ref")
	}
	if runs := srv.registry.RunsForItem(ref); len(runs) != 1 {
		t.Errorf("RunsForItem = %d runs, want 1", len(runs))
	}
}

func TestRunItemNonPRUsesRequestRepo(t *testing.T) {
	repoDir := t.TempDir()
	// A Linear-style item carries no `repo` var, so the request supplies it.
	fs := &fakeSource{getItem: source.Item{
		Ref:  engine.ItemRef{Source: "linear", Type: "issue", ExternalID: "abc-1"},
		Vars: map[string]string{"identifier": "ENG-42"},
	}}
	repos := config.Repos{"allouis/attractor": repoDir}
	srv := itemsServerWithRepos(t, map[string]source.Source{"linear": fs}, repos)

	resp, id := postRunItem(t, srv.URL(), map[string]any{
		"item_ref": engine.ItemRef{Source: "linear", Type: "issue", ExternalID: "abc-1"},
		"pipeline": writeGraph(t),
		"repo":     "allouis/attractor",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	summary := pollRunSummary(t, srv.URL(), id)
	if got, _ := summary["cwd"].(string); got != repoDir {
		t.Errorf("run cwd = %q, want %q (repo from request)", got, repoDir)
	}
}

func TestRunItemUnknownSource(t *testing.T) {
	srv := itemsServer(t, map[string]source.Source{})
	resp, _ := postRunItem(t, srv.URL(), map[string]any{
		"item_ref": engine.ItemRef{Source: "nope", Type: "pr", ExternalID: "x#1"},
		"pipeline": writeGraph(t),
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestRunItemRepoUnmapped(t *testing.T) {
	fs := &fakeSource{getItem: prItem()}
	srv := itemsServerWithRepos(t, map[string]source.Source{"github": fs}, config.Repos{})
	resp, _ := postRunItem(t, srv.URL(), map[string]any{
		"item_ref": engine.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"},
		"pipeline": writeGraph(t),
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

func TestRunItemNoRepo(t *testing.T) {
	// Non-PR item with no repo var and no request repo → unresolvable.
	fs := &fakeSource{getItem: source.Item{
		Ref: engine.ItemRef{Source: "linear", Type: "issue", ExternalID: "abc-1"},
	}}
	srv := itemsServerWithRepos(t, map[string]source.Source{"linear": fs}, config.Repos{})
	resp, _ := postRunItem(t, srv.URL(), map[string]any{
		"item_ref": engine.ItemRef{Source: "linear", Type: "issue", ExternalID: "abc-1"},
		"pipeline": writeGraph(t),
	})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

func TestRunItemGetFails(t *testing.T) {
	fs := &fakeSource{getErr: errors.New("gh: not found")}
	srv := itemsServerWithRepos(t, map[string]source.Source{"github": fs}, config.Repos{})
	resp, _ := postRunItem(t, srv.URL(), map[string]any{
		"item_ref": engine.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"},
		"pipeline": writeGraph(t),
	})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func getItems(t *testing.T, url string) (*http.Response, []map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp, body.Items
}

func TestItemsListReturnsItems(t *testing.T) {
	fs := &fakeSource{items: []source.Item{
		{Ref: engine.ItemRef{Source: "github", Type: "pr", ExternalID: "o/r#1"}, Title: "One"},
	}}
	srv := itemsServer(t, map[string]source.Source{"github": fs})

	resp, items := getItems(t, srv.URL()+"/items?source=github")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(items) != 1 || items[0]["title"] != "One" {
		t.Fatalf("items = %+v", items)
	}
	if ip, _ := items[0]["in_progress"].(bool); ip {
		t.Error("item with no linked run should not be in_progress")
	}
}

func TestItemsListFilterAssigned(t *testing.T) {
	fs := &fakeSource{}
	srv := itemsServer(t, map[string]source.Source{"github": fs})
	getItems(t, srv.URL()+"/items?source=github&filter=assigned")
	if !fs.filter.Assigned {
		t.Error("filter=assigned did not reach the source")
	}
}

func TestItemsListAnnotatesLinkedRuns(t *testing.T) {
	ref := engine.ItemRef{Source: "github", Type: "pr", ExternalID: "o/r#1"}
	fs := &fakeSource{items: []source.Item{{Ref: ref, Title: "One"}}}
	srv := itemsServer(t, map[string]source.Source{"github": fs})

	// A queued run carrying the same item_ref makes the item in-progress.
	run := srv.registry.NewRun("", nil, nil, srv.registry.baseDir, nil, &ref)

	_, items := getItems(t, srv.URL()+"/items?source=github")
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	if ip, _ := items[0]["in_progress"].(bool); !ip {
		t.Error("item with a queued linked run should be in_progress")
	}
	linked, _ := items[0]["linked_runs"].([]any)
	if len(linked) != 1 {
		t.Fatalf("linked_runs = %+v", items[0]["linked_runs"])
	}
	if got := linked[0].(map[string]any)["id"]; got != run.ID {
		t.Errorf("linked run id = %v, want %v", got, run.ID)
	}
}

func TestItemsListUnknownSource(t *testing.T) {
	srv := itemsServer(t, map[string]source.Source{})
	resp, _ := getItems(t, srv.URL()+"/items?source=nope")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestItemsListSourceError(t *testing.T) {
	fs := &fakeSource{err: errors.New("gh: not authenticated")}
	srv := itemsServer(t, map[string]source.Source{"github": fs})
	resp, _ := getItems(t, srv.URL()+"/items?source=github")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}

func TestRunsForItem(t *testing.T) {
	reg := newRunRegistry(t.TempDir())
	ref := engine.ItemRef{Source: "github", Type: "pr", ExternalID: "o/r#1"}
	other := engine.ItemRef{Source: "github", Type: "pr", ExternalID: "o/r#2"}
	a := reg.NewRun("", nil, nil, reg.baseDir, nil, &ref)
	reg.NewRun("", nil, nil, reg.baseDir, nil, &other)
	reg.NewRun("", nil, nil, reg.baseDir, nil, nil)

	got := reg.RunsForItem(ref)
	if len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("RunsForItem = %+v, want [%s]", got, a.ID)
	}
}
