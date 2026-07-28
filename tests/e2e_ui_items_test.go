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
