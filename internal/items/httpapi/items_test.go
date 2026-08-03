package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/items"
	"github.com/allouis/attractor/internal/items/source"
)

// fakeSource is an injectable Source recording the filter/ref it was
// called with and replaying canned items.
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

// fakeDeps stands in for the server: it replays canned sources and the runs
// linked to an item tag.
type fakeDeps struct {
	sources map[string]source.Source
	linked  map[string][]LinkedRun
}

func (d *fakeDeps) Source(name string) (source.Source, bool) {
	s, ok := d.sources[name]
	return s, ok
}

func (d *fakeDeps) SourceNames() []string {
	names := make([]string, 0, len(d.sources))
	for name := range d.sources {
		names = append(names, name)
	}
	return names
}

func (d *fakeDeps) LinkedRuns(tag string) []LinkedRun { return d.linked[tag] }

// itemsServer mounts Register on a test HTTP server backed by deps.
func itemsServer(t *testing.T, deps *fakeDeps) string {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, deps)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL
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
		{Ref: items.ItemRef{Source: "github", Type: "pr", ExternalID: "o/r#1"}, Title: "One"},
	}}
	url := itemsServer(t, &fakeDeps{sources: map[string]source.Source{"github": fs}})

	resp, list := getItems(t, url+"/items?source=github")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(list) != 1 || list[0]["title"] != "One" {
		t.Fatalf("items = %+v", list)
	}
	if ip, _ := list[0]["in_progress"].(bool); ip {
		t.Error("item with no linked run should not be in_progress")
	}
}

func TestItemsListFilterAssigned(t *testing.T) {
	fs := &fakeSource{}
	url := itemsServer(t, &fakeDeps{sources: map[string]source.Source{"github": fs}})
	getItems(t, url+"/items?source=github&filter=assigned")
	if !fs.filter.Assigned {
		t.Error("filter=assigned did not reach the source")
	}
}

func TestItemsListAnnotatesLinkedRuns(t *testing.T) {
	ref := items.ItemRef{Source: "github", Type: "pr", ExternalID: "o/r#1"}
	fs := &fakeSource{items: []source.Item{{Ref: ref, Title: "One"}}}
	// A queued run carrying the same item_ref makes the item in-progress.
	deps := &fakeDeps{
		sources: map[string]source.Source{"github": fs},
		linked:  map[string][]LinkedRun{ref.String(): {{ID: "run-9", Status: "queued", Active: true}}},
	}
	url := itemsServer(t, deps)

	_, list := getItems(t, url+"/items?source=github")
	if len(list) != 1 {
		t.Fatalf("items = %+v", list)
	}
	if ip, _ := list[0]["in_progress"].(bool); !ip {
		t.Error("item with a queued linked run should be in_progress")
	}
	linked, _ := list[0]["linked_runs"].([]any)
	if len(linked) != 1 {
		t.Fatalf("linked_runs = %+v", list[0]["linked_runs"])
	}
	if got := linked[0].(map[string]any)["id"]; got != "run-9" {
		t.Errorf("linked run id = %v, want run-9", got)
	}
}

func TestItemsListUnknownSource(t *testing.T) {
	url := itemsServer(t, &fakeDeps{sources: map[string]source.Source{}})
	resp, _ := getItems(t, url+"/items?source=nope")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func getSources(t *testing.T, url string) (*http.Response, []string) {
	t.Helper()
	resp, err := http.Get(url + "/items/sources")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Sources []string `json:"sources"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp, body.Sources
}

// TestListSources proves GET /items/sources reports the configured source
// names, sorted, so the UI discovers what to fetch instead of hardcoding
// the list (web-ui-spec W3, review B2).
func TestListSources(t *testing.T) {
	deps := &fakeDeps{sources: map[string]source.Source{
		"linear": &fakeSource{},
		"github": &fakeSource{},
	}}
	url := itemsServer(t, deps)

	resp, got := getSources(t, url)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	want := []string{"github", "linear"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sources = %v, want %v (sorted)", got, want)
	}
}

// TestListSourcesEmpty proves an unconfigured daemon reports an empty list
// (JSON []), not null — the UI must distinguish "no sources" from an error.
func TestListSourcesEmpty(t *testing.T) {
	url := itemsServer(t, &fakeDeps{sources: map[string]source.Source{}})
	resp, err := http.Get(url + "/items/sources")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if got := strings.TrimSpace(string(body)); got != `{"sources":[]}` {
		t.Errorf("body = %s, want {\"sources\":[]}", got)
	}
}

func TestItemsListSourceError(t *testing.T) {
	fs := &fakeSource{err: errors.New("gh: not authenticated")}
	url := itemsServer(t, &fakeDeps{sources: map[string]source.Source{"github": fs}})
	resp, _ := getItems(t, url+"/items?source=github")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
}
