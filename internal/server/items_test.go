package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/fabro/attractor/internal/engine"
	"github.com/fabro/attractor/internal/source"
)

// fakeSource is an injectable Source recording the filter it was called
// with and replaying canned items.
type fakeSource struct {
	items  []source.Item
	err    error
	filter source.Filter
}

func (f *fakeSource) List(_ context.Context, filter source.Filter) ([]source.Item, error) {
	f.filter = filter
	return f.items, f.err
}

func itemsServer(t *testing.T, sources map[string]source.Source) *Server {
	t.Helper()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir(), Sources: sources})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
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
