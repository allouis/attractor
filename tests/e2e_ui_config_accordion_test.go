package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_UI_ConfigAccordion guards the T7b collapse rule (ui-tailwind-spec
// T7b): on a phone the Config repo/provider rows are accordions — a collapsed
// row hides its editable detail cells behind the summary header. That rule
// lives in ui/input.css (a mobile-scoped `tr.is-collapsed .accordion-detail`
// hide) and compiles into the injected tailwind.css, so it appears in the
// served /ui page — proof the committed stylesheet was regenerated from
// input.css (dev/test parity, like the T7a guard). The interactive toggle and
// the 390px no-overflow are verified by ui_review / hand (agent-browser).
func TestServer_UI_ConfigAccordion(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	// Assert the compiled rule, not the bare class names — those also appear in
	// the served markup, so only the `{display:none}` body proves the stylesheet
	// carries the collapse rule (i.e. was regenerated from input.css).
	if !strings.Contains(page, ".accordion-detail{display:none}") {
		t.Errorf("injected stylesheet missing the accordion collapse rule (stale tailwind.css?)")
	}
	if !strings.Contains(page, ".is-collapsed") {
		t.Errorf("injected stylesheet missing the is-collapsed selector (stale tailwind.css?)")
	}
}
