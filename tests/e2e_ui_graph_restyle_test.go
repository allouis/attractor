package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_UI_GraphRestyle guards the run-graph restyle (ui-tailwind-spec
// T4a): the inline graphviz SVG is themed with the `--` tokens — edges use
// --text-muted/--border with rounded caps, nodes use --surface-1/--border, text
// uses --text + the UI font — so it is theme-aware and never hardcodes
// black/white. The rules live in ui/input.css and compile into the injected
// tailwind.css, so they appear in the served /ui page. Because status colour is
// painted onto node strokes at runtime, paintNode must set an INLINE style
// (which beats the stylesheet) rather than a presentation attribute (which the
// new .node rule would override) — else running/failed nodes lose their colour.
func TestServer_UI_GraphRestyle(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	// The injected (minified) stylesheet must scope graph rules to .graph-pane
	// and drive stroke/fill from tokens — proof the committed tailwind.css was
	// regenerated from input.css (dev/test parity, like the T1–T3 guards).
	for _, frag := range []string{
		".graph-pane",
		"stroke:var(--text-muted)",
		"stroke:var(--border)",
		"fill:var(--surface-1)",
		"fill:var(--text)",
	} {
		if !strings.Contains(page, frag) {
			t.Errorf("injected stylesheet missing graph restyle rule %q (stale tailwind.css?)", frag)
		}
	}

	// Status paint must survive the new .node stroke rule: inline style wins over
	// the stylesheet; a presentation attribute would not.
	paint := sliceFunc(t, page, "paintNode")
	if !strings.Contains(paint, ".style.stroke") {
		t.Errorf("paintNode must set inline .style.stroke so status colour beats the graph CSS:\n%s", paint)
	}
	if strings.Contains(paint, "setAttribute('stroke'") {
		t.Errorf("paintNode still sets stroke via presentation attribute; the .node rule will override it:\n%s", paint)
	}
}
