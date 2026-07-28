package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// uiPage fetches the embedded single-page UI served at /ui.
func uiPage(t *testing.T) string {
	t.Helper()
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))
	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(body)
}

// TestServer_UI_Items_List guards the Items view list scaffolding
// (web-ui-spec W3): the render target plus the per-source fetch + client
// merge + render JS the view binds to. Static markers; the runtime
// two-source merge is driven separately.
func TestServer_UI_Items_List(t *testing.T) {
	page := uiPage(t)

	if !strings.Contains(page, `id="items-list"`) {
		t.Errorf("page missing items render target id=\"items-list\"")
	}
	for _, fn := range []string{"fetchItems", "renderItems"} {
		if !strings.Contains(page, fn) {
			t.Errorf("page missing items JS %q", fn)
		}
	}
}

// TestServer_UI_Items_SourceDiscovery guards that the Items view discovers
// its sources from the backend (GET /items/sources) instead of a hardcoded
// list, and surfaces per-source load failures rather than silently dropping
// them (web-ui-spec W3, review B1/B2). Structural markers; the runtime
// partial-failure banner is driven separately.
func TestServer_UI_Items_SourceDiscovery(t *testing.T) {
	page := uiPage(t)

	if strings.Contains(page, "ITEM_SOURCES") {
		t.Errorf("page still hardcodes ITEM_SOURCES — sources must come from /items/sources")
	}
	for _, marker := range []string{"/items/sources", "renderSourceWarning", `id="items-warning"`} {
		if !strings.Contains(page, marker) {
			t.Errorf("page missing source-discovery marker %q", marker)
		}
	}
}

// TestServer_UI_Items_Badges guards the linked-run / in-progress badge on
// an item row (web-ui-spec W3): the badge markup hook and its styling. The
// badge links to #run/<id> so a click jumps to the linked run.
func TestServer_UI_Items_Badges(t *testing.T) {
	page := uiPage(t)

	if !strings.Contains(page, ".badge") {
		t.Errorf("page missing badge styling")
	}
	if !strings.Contains(page, "itemBadges") {
		t.Errorf("page missing itemBadges JS")
	}
}

// TestServer_UI_Items_Filters guards the frontend filter + sort/group
// controls (web-ui-spec W3): source/type/repo/text/in-progress narrowing
// and group-by, applied client-side over the merged set.
func TestServer_UI_Items_Filters(t *testing.T) {
	page := uiPage(t)

	for _, id := range []string{
		`id="items-filter-source"`,
		`id="items-filter-type"`,
		`id="items-filter-repo"`,
		`id="items-filter-text"`,
		`id="items-filter-progress"`,
		`id="items-group"`,
	} {
		if !strings.Contains(page, id) {
			t.Errorf("page missing filter control %q", id)
		}
	}
	if !strings.Contains(page, "applyFilters") {
		t.Errorf("page missing applyFilters JS")
	}
}
