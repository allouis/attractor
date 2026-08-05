package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_UI_GraphPolish guards the run-graph polish (ui-tailwind-spec T7a):
// on top of the T4a token restyle, the SVG gets rounded stroke joins on
// nodes/edges and a comfortable UI text size/weight, and the expanded
// sub-workflow cluster border is themed from tokens. These rules live in
// ui/input.css and compile into the injected tailwind.css, so they appear in
// the served /ui page — proof the committed stylesheet was regenerated
// (dev/test parity, like the T4a guard). The visual quality (light + dark) is
// verified by the ui_review node; the 390px no-overflow by hand (agent-browser).
func TestServer_UI_GraphPolish(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	for _, frag := range []string{
		"stroke-linejoin:round",
		"font-weight:500",
	} {
		if !strings.Contains(page, frag) {
			t.Errorf("injected stylesheet missing graph polish rule %q (stale tailwind.css?)", frag)
		}
	}
}
