package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_UI_WorkflowsView guards the Workflows view mounts (web-ui-spec
// W6): a JS-filled catalog list and a click-through detail section with a
// graph pane. The list is populated from GET /workflows and the graph from
// GET /workflows/{name}/graph at runtime; here we only assert the static
// page carries the mount points the JS binds to (the same convention as the
// nav-shell guard — JS behaviour is verified separately).
func TestServer_UI_WorkflowsView(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	for _, id := range []string{`id="workflows-list"`, `id="workflow-view"`, `id="workflow-graph"`} {
		if !strings.Contains(page, id) {
			t.Errorf("page missing Workflows-view mount %q", id)
		}
	}
}
