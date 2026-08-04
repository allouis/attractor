package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_UI_GraphEngineSelector guards the run-detail engine selector
// (ui-tailwind-spec T4b): a dropdown lets the viewer re-render the run graph
// with a chosen graphviz layout engine. The six allowlisted engines appear as
// options (default dot), loadGraph forwards the choice as ?engine=, and a change
// handler re-renders the current run. The control is Tailwind-styled with a
// ≥40px touch target, so its min-h utility must be in the injected stylesheet
// (dev/test parity, like the T1–T3 guards).
func TestServer_UI_GraphEngineSelector(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	if !strings.Contains(page, `id="graph-engine"`) {
		t.Fatalf("run view missing the #graph-engine selector")
	}
	for _, eng := range []string{"dot", "neato", "fdp", "sfdp", "circo", "twopi"} {
		if !strings.Contains(page, `value="`+eng+`"`) {
			t.Errorf("engine selector missing option %q", eng)
		}
	}

	// loadGraph forwards the chosen engine to the graph endpoint.
	load := sliceFunc(t, page, "loadGraph")
	for _, frag := range []string{"graph-engine", "engine="} {
		if !strings.Contains(load, frag) {
			t.Errorf("loadGraph does not forward the chosen engine (missing %q):\n%s", frag, load)
		}
	}

	// A change on the selector re-renders the current run's graph.
	if !strings.Contains(page, `$('graph-engine')?.addEventListener('change'`) {
		t.Errorf("engine selector is not wired to re-render on change")
	}

	// The Tailwind touch-target utility must be present in the injected
	// stylesheet — proof the committed tailwind.css was regenerated.
	// The min-h-10 touch target. Tailwind v3 emitted 2.5rem, v4 emits
	// calc(var(--spacing) * 10); accept either so the guard tracks the utility.
	if !strings.Contains(page, "min-height:2.5rem") && !strings.Contains(page, "min-height:calc(var(--spacing) * 10)") {
		t.Errorf("injected stylesheet has no min-h-10 (≥40px touch target) utility (stale tailwind.css?)")
	}
}
