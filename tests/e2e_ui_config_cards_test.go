package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_UI_ConfigCards guards the responsive Config tables (ui-tailwind-spec
// T2): the Repos and Providers editable tables overflow (~572px) on a phone, so
// at ≤640px each row renders as a stacked card (label/value per line) while the
// desktop table survives at ≥640px. The panels are client-side JS template
// literals embedded in the served page, so their utility classes appear in the
// /ui source just like the static header — the 390px no-overflow is verified by
// hand (agent-browser); here we guard the markup + that the injected stylesheet
// actually defines the block↔table utilities the cards rely on.
func TestServer_UI_ConfigCards(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	// Both panels render through the shared dataTable shell, which switches the
	// table from a stacked block on mobile to a real table at the sm breakpoint
	// and hides the header row on mobile (each card carries its own field labels).
	table := sliceFunc(t, page, "dataTable")
	for _, cls := range []string{"block", "sm:table", "hidden", "sm:table-header-group"} {
		if !strings.Contains(table, cls) {
			t.Errorf("dataTable missing responsive class %q (block→table swap):\n%s", cls, table)
		}
	}

	// Each row builder renders a shared card row (CARD_ROW_CLASS) of card cells
	// (cardCell) with full-width mobile inputs, restored to a table row at sm.
	for _, fn := range []string{"repoRowHtml", "providerRowHtml"} {
		body := sliceFunc(t, page, fn)
		for _, ref := range []string{"CARD_ROW_CLASS", "cardCell(", "w-full"} {
			if !strings.Contains(body, ref) {
				t.Errorf("%s no longer builds a stacked card via %q:\n%s", fn, ref, body)
			}
		}
	}

	// The shared card primitives carry the token-mapped card classes, shipped in
	// the served bundle.
	for _, cls := range []string{"sm:table-row", "bg-surface-1", "border-line", "sm:hidden"} {
		if !strings.Contains(page, cls) {
			t.Errorf("served page missing card class %q (stacked card at ≤640px)", cls)
		}
	}

	// The injected stylesheet must define the utilities the markup relies on:
	// proves the committed tailwind.css was regenerated from the new classes, so
	// serveUI ships working CSS (dev/test parity, like the T1 guard).
	if !strings.Contains(page, "display:table-header-group") {
		t.Errorf("injected stylesheet defines no .table-header-group utility (stale tailwind.css?)")
	}
}

// sliceFunc returns the source of the named top-level JS function embedded in
// the page (from `function name` to the next top-level `\nfunction `), so an
// assertion scopes to one panel builder instead of matching a class page-wide.
func sliceFunc(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "function "+name)
	if start < 0 {
		t.Fatalf("function %s not found in page", name)
	}
	rest := src[start+len("function "+name):]
	end := strings.Index(rest, "\nfunction ")
	if end < 0 {
		return src[start:]
	}
	return src[start : start+len("function "+name)+end]
}
