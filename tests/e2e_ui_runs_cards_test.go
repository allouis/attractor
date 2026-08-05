package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_UI_RunsCards guards the responsive Runs table (ui-tailwind-spec
// T3, tightened by T8a): at ≤640px each run collapses to a tight, glanceable
// summary — a mobile-only tappable link (short id, workflow, status badge,
// relative time) instead of a per-field label stack — while the full desktop
// table survives at ≥640px. Like the T2 config guard, the panel is a
// client-side JS template literal in the served page, so its utility classes
// appear in the /ui source; the 390px no-overflow is verified by hand
// (agent-browser) and here we guard the markup + that the injected stylesheet
// defines the block↔table utilities the cards rely on.
func TestServer_UI_RunsCards(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	// The block→table swap lives in the shared dataTable shell: a stacked block
	// on mobile, a real table at sm, header row hidden on mobile (each card
	// carries its own field labels).
	table := sliceFunc(t, page, "dataTable")
	for _, cls := range []string{"block", "sm:table", "hidden", "sm:table-header-group", "w-full"} {
		if !strings.Contains(table, cls) {
			t.Errorf("dataTable missing responsive class %q (block→table swap):\n%s", cls, table)
		}
	}

	// runRowHtml builds each run as a shared card row (CARD_ROW_CLASS) with a
	// compact mobile summary (runSummaryCell) plus desktop-only per-field cells
	// (runDetailCell) — the per-field label stack is dropped on mobile (T8a).
	row := sliceFunc(t, page, "runRowHtml")
	for _, ref := range []string{"CARD_ROW_CLASS", "runSummaryCell(", "runDetailCell("} {
		if !strings.Contains(row, ref) {
			t.Errorf("runRowHtml no longer builds the compact T8a row via %q:\n%s", ref, row)
		}
	}
	// The `by` field (origin) the desktop table clips survives as a desktop-only
	// cell, restored at ≥sm rather than shown as a mobile label.
	if !strings.Contains(row, "runOriginCell") {
		t.Errorf("runRowHtml drops the `by` (origin) field:\n%s", row)
	}
	// The whole mobile summary is one tap-target link to the run detail.
	summary := sliceFunc(t, page, "runSummaryCell")
	for _, ref := range []string{"block sm:hidden", "#run/"} {
		if !strings.Contains(summary, ref) {
			t.Errorf("runSummaryCell is not a mobile-only link to the run detail (missing %q):\n%s", ref, summary)
		}
	}

	// The shared card primitives carry the token-mapped card classes, shipped in
	// the served bundle (restored to a table row at sm).
	for _, cls := range []string{"sm:table-row", "bg-surface-1", "border-line", "sm:hidden"} {
		if !strings.Contains(page, cls) {
			t.Errorf("served page missing card class %q (stacked card at ≤640px)", cls)
		}
	}

	// The injected stylesheet must define the utilities the markup relies on:
	// proves the committed tailwind.css was regenerated from the new classes, so
	// serveUI ships working CSS (dev/test parity, like the T1/T2 guards).
	if !strings.Contains(page, "display:table-header-group") {
		t.Errorf("injected stylesheet defines no .table-header-group utility (stale tailwind.css?)")
	}
}
