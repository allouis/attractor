package server

import (
	"strings"
	"testing"
)

// TestRunRowCompactMobileSummary: on ≤640px each run collapses to a tight,
// glanceable summary (ui-tailwind-spec T8a) — a mobile-only cell that is a
// single tappable link to the run detail, carrying the short id, the workflow
// label, a status badge and the relative time. The per-field label stack is
// dropped on mobile; extra fields live in the detail view.
func TestRunRowCompactMobileSummary(t *testing.T) {
	html := evalUI(t, `runsTableHtml([{id:"deadbeefcafe1234",workflow_name:"ship-it",status:"running",started_at:"2026-07-25"}])`)

	// A mobile-only summary cell: shown <sm, hidden ≥sm (the desktop columns take over).
	if !strings.Contains(html, "block sm:hidden") {
		t.Errorf("runs list missing the mobile-only compact summary cell (block sm:hidden):\n%s", html)
	}
	// The whole summary is one tap-target link to the run detail.
	if !strings.Contains(html, `href="#run/deadbeefcafe1234"`) {
		t.Errorf("compact summary is not a link to the run detail:\n%s", html)
	}
	// Glanceable fields: short id (first 8), workflow label, status badge, relative time.
	for _, want := range []string{"deadbeef", "ship-it", "status running"} {
		if !strings.Contains(html, want) {
			t.Errorf("compact summary missing %q:\n%s", want, html)
		}
	}
}

// TestRunRowFieldsDesktopOnly: the per-field cells become desktop-only so the
// mobile card shows only the compact summary, not a label/value stack per field
// (ui-tailwind-spec T8a). The provenance columns render as hidden ≥sm-restored
// cells, and the old mobile field-label spans are gone.
func TestRunRowFieldsDesktopOnly(t *testing.T) {
	html := evalUI(t, `runsTableHtml([{id:"deadbeefcafe1234",workflow_name:"ship-it",status:"running",started_at:"2026-07-25"}])`)

	// Detail cells are hidden on mobile, restored as real cells at ≥sm.
	if !strings.Contains(html, `hidden border-0 sm:table-cell`) {
		t.Errorf("run detail cells are not desktop-only (want hidden border-0 sm:table-cell):\n%s", html)
	}
	// The dropped per-field label stack: cardCell's mobile labels must not appear
	// in a run row (the summary carries the glanceable fields instead).
	if strings.Contains(html, `class="block text-muted text-sm sm:hidden">workflow`) {
		t.Errorf("run row still emits the per-field mobile label stack:\n%s", html)
	}
}

// TestRunRowCancelIsDesktopOnly: the cancel control stays a desktop-table cell,
// not surfaced on the compact mobile summary — a live run keeps a cancel button
// in a desktop-only cell (cancel from the detail view on mobile), and a terminal
// run offers none (ui-tailwind-spec T8a; supersedes the T6b empty-cancel-band).
func TestRunRowCancelIsDesktopOnly(t *testing.T) {
	live := evalUI(t, `runsTableHtml([{id:"deadbeefcafe1234",status:"running",started_at:"2026-07-25"}])`)
	if !strings.Contains(live, "data-cancel") {
		t.Errorf("live run lost its cancel control:\n%s", live)
	}
	// The cancel button rides a desktop-only cell, never the mobile summary link.
	if !strings.Contains(live, `<td class="hidden border-0 sm:table-cell"><button data-cancel`) {
		t.Errorf("cancel button is not confined to a desktop-only cell:\n%s", live)
	}
	term := evalUI(t, `runsTableHtml([{id:"deadbeefcafe1234",status:"completed",started_at:"2026-07-25"}])`)
	if strings.Contains(term, "data-cancel") {
		t.Errorf("terminal run wrongly offers a cancel control:\n%s", term)
	}
}
