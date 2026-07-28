package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/items"
	"github.com/allouis/attractor/internal/items/source"
)

// The items HTTP handlers live in internal/items/httpapi and are tested
// there against a fake Deps. What remains here is the registry-level
// RunsForItem test plus the shared fakes that back the items e2e tests
// (router_dispatch_test.go), which drive the handlers through a live
// server to prove the mount.

// fakeSource is an injectable Source recording the filter it was called
// with and replaying canned items.
type fakeSource struct {
	items   []source.Item
	err     error
	filter  source.Filter
	getItem source.Item
	getErr  error
	gotRef  items.ItemRef
}

func (f *fakeSource) List(_ context.Context, filter source.Filter) ([]source.Item, error) {
	f.filter = filter
	return f.items, f.err
}

func (f *fakeSource) Get(_ context.Context, ref items.ItemRef) (source.Item, error) {
	f.gotRef = ref
	return f.getItem, f.getErr
}

func itemsServerWithRepos(t *testing.T, sources map[string]source.Source, repos items.Repos) *Server {
	t.Helper()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir(), Sources: sources, Repos: repos})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
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
		Ref:   items.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"},
		Title: "Fix login",
		Vars: map[string]string{
			"repo":      "allouis/attractor",
			"pr_number": "42",
		},
	}
}

func TestRunsForItem(t *testing.T) {
	reg := newRunRegistry(t.TempDir())
	tag := "github:pr:o/r#1"
	other := "github:pr:o/r#2"
	// Two runs for the same item must group together; a different tag and
	// a bare (untagged) run are excluded.
	a := reg.NewRun("", nil, nil, reg.baseDir, nil, tag, nil)
	b := reg.NewRun("", nil, nil, reg.baseDir, nil, tag, nil)
	reg.NewRun("", nil, nil, reg.baseDir, nil, other, nil)
	reg.NewRun("", nil, nil, reg.baseDir, nil, "", nil)

	got := reg.RunsForItem(tag)
	if len(got) != 2 {
		t.Fatalf("RunsForItem = %+v, want 2 runs for %s", got, tag)
	}
	ids := map[string]bool{got[0].ID: true, got[1].ID: true}
	if !ids[a.ID] || !ids[b.ID] {
		t.Fatalf("RunsForItem = %+v, want runs %s and %s", got, a.ID, b.ID)
	}
}
