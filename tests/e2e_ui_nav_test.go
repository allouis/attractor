package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_UI_NavShell guards the nav shell (web-ui-spec W1): a 3-tab
// header (Items/Runs/Workflows) over hash routing, with a mount point for
// each top-level view. The Items/Workflows views are placeholders that
// W3/W6 fill; the existing Runs list moves under the Runs view. The
// default-landing-Items behaviour is JS runtime and verified separately.
func TestServer_UI_NavShell(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	// Nav links for the three top-level tabs.
	for _, href := range []string{`href="#items"`, `href="#runs"`, `href="#workflows"`} {
		if !strings.Contains(page, href) {
			t.Errorf("page missing nav link %q", href)
		}
	}
	// A mount point per view; W3/W6 render into items/workflows.
	for _, id := range []string{`id="items-view"`, `id="runs-view"`, `id="workflows-view"`} {
		if !strings.Contains(page, id) {
			t.Errorf("page missing view mount %q", id)
		}
	}
}
