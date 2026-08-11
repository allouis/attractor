package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_UI_GraphPaintFromState guards the R4 hydrate-then-append rewrite:
// node status is painted STRICTLY from GET /state, never from the SSE stream.
// The old client-side nodeState accumulator (the source of the refresh bug) is
// gone; the graph paints via paintGraphFromState off the last /state doc, and
// lifecycle SSE events trigger a debounced /state re-fetch (scheduleStateRehydrate)
// rather than mutating client state directly. This is what makes a mid-view
// refresh idempotent.
func TestServer_UI_GraphPaintFromState(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	// The order-sensitive replay accumulator is deleted — no symbol survives.
	if strings.Contains(page, "nodeState") {
		t.Errorf("the client-side nodeState accumulator must be gone (refresh idempotency)")
	}
	// Painting is state-driven and re-hydration is SSE-driven-but-debounced.
	for _, want := range []string{"paintGraphFromState", "scheduleStateRehydrate", "rehydrateState"} {
		if !strings.Contains(page, want) {
			t.Errorf("state-driven graph painting missing %q", want)
		}
	}
	// The SSE lifecycle handlers no longer paint directly; they schedule a
	// /state re-fetch. A bare paintNode('running') call in a handler would be
	// the old replay-derived path.
	if strings.Contains(page, "paintNode(ev.node_id") {
		t.Errorf("SSE handlers must not paint from the event stream; re-hydrate from /state instead")
	}
}

// TestServer_UI_GraphAsMap guards the R4 "graph as map" styling: the run graph
// is height-capped and scrollable (no more two-screen sprawl), node status is
// tinted from the /state-painted classes, and the active pulse honours
// prefers-reduced-motion.
func TestServer_UI_GraphAsMap(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	for _, want := range []string{
		"#graph { max-height: 40vh; overflow: auto; }", // capped, scrollable map
		"g.node-completed", // status tint classes exist
		"g.node-failed",
		"g.node-active",
		"@keyframes node-pulse",          // active pulse
		"prefers-reduced-motion: reduce", // pulse honours reduced motion
	} {
		if !strings.Contains(page, want) {
			t.Errorf("graph-as-map styling missing %q", want)
		}
	}
}
