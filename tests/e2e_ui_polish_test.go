package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_UI_Polish guards the stage-inspector polish (ui-tailwind-spec T5):
// the code/prompt/response/output/diff blocks scroll in-block (overflow-x-auto)
// and their containers can shrink below content width (min-w-0), so a wide
// payload never pushes the page past the viewport at 390px; the interactive
// filter/config/modal controls are ≥40px tall touch targets; and the
// event-filter row wraps instead of overflowing. Like the T1–T4 guards the
// classes appear in the served /ui markup and the injected (minified)
// tailwind.css must define the overflow/min-width utilities they rely on —
// proof the committed stylesheet was regenerated (dev/test parity). The 390px
// no-overflow is verified by hand (agent-browser).
func TestServer_UI_Polish(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	// The injected stylesheet must define the overflow/min-width utilities the
	// inspector blocks rely on: proves the committed tailwind.css was
	// regenerated from the new classes (dev/test parity, like the T1–T4 guards).
	for _, frag := range []string{"overflow-x:auto", ".min-w-0{min-width:0}"} {
		if !strings.Contains(page, frag) {
			t.Errorf("injected stylesheet missing utility %q (stale tailwind.css?)", frag)
		}
	}

	// The inspector card and the run-diff container are grid children: without
	// min-w-0 they refuse to shrink below their content, so a wide block pushes
	// the whole page past the viewport.
	for _, want := range []string{`class="card min-w-0"`, `class="diff min-w-0"`} {
		if !strings.Contains(page, want) {
			t.Errorf("markup missing %q (grid child can't shrink → page overflow at 390px)", want)
		}
	}

	// The prompt/response/output blocks scroll horizontally in-block rather than
	// overflow: the stage-text pre and the live-tail node-log both carry
	// overflow-x-auto.
	detail := sliceFunc(t, page, "stageDetailHtml")
	for _, cls := range []string{"stage-text overflow-x-auto", `id="node-log" class="overflow-x-auto"`} {
		if !strings.Contains(detail, cls) {
			t.Errorf("stageDetailHtml missing %q (inspector block does not scroll):\n%s", cls, detail)
		}
	}
	if tc := sliceFunc(t, page, "toolCallHtml"); !strings.Contains(tc, "overflow-x-auto") {
		t.Errorf("toolCallHtml pre not overflow-x-auto (wide JSON payload overflows):\n%s", tc)
	}

	// Interactive controls are ≥40px touch targets: the toolbar, config, modal
	// and button rules each set min-height: 2.5rem.
	if n := strings.Count(page, "min-height: 2.5rem"); n < 3 {
		t.Errorf("only %d control rules set a ≥40px touch target (want the toolbar/config/modal/button rules)", n)
	}

	// The event-filter row wraps instead of overflowing at a narrow width.
	if rule := styleRule(t, page, ".tl-controls"); !strings.Contains(rule, "flex-wrap: wrap") {
		t.Errorf(".tl-controls does not wrap (filter row overflows at ≤640px):\n%s", rule)
	}
}

// styleRule returns the body of the first CSS rule for the exact selector in the
// page's <style> block (from `selector {` to the next `}`), so an assertion
// scopes to one rule instead of matching a declaration page-wide.
func styleRule(t *testing.T, page, selector string) string {
	t.Helper()
	start := strings.Index(page, selector+" {")
	if start < 0 {
		t.Fatalf("style rule %q not found in page", selector)
	}
	rest := page[start:]
	end := strings.Index(rest, "}")
	if end < 0 {
		return rest
	}
	return rest[:end]
}
