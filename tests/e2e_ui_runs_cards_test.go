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
// T3): the fleet table clips the `by` column on a phone, so at ≤640px each run
// renders as a stacked card (run id, workflow, item, repo, by, status, started)
// with no clipped columns while the desktop table survives at ≥640px. Like the
// T2 config guard, the panel is a client-side JS template literal in the served
// page, so its utility classes appear in the /ui source; the 390px no-overflow
// is verified by hand (agent-browser) and here we guard the markup + that the
// injected stylesheet defines the block↔table utilities the cards rely on.
func TestServer_UI_RunsCards(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	// The table swaps from a stacked block on mobile to a real table at sm, and
	// hides the header row on mobile (each card carries its own field labels).
	table := sliceFunc(t, page, "runsTableHtml")
	for _, cls := range []string{"block", "sm:table", "hidden", "sm:table-header-group", "w-full"} {
		if !strings.Contains(table, cls) {
			t.Errorf("runsTableHtml missing responsive class %q (block→table swap):\n%s", cls, table)
		}
	}

	// Each run is a card on mobile (token-mapped surface + border, restored to a
	// table row at sm) with mobile-only field labels — including the `by` field
	// the desktop table clips.
	row := sliceFunc(t, page, "runRowHtml")
	for _, cls := range []string{"sm:table-row", "bg-surface-1", "border-line", "sm:hidden"} {
		if !strings.Contains(row, cls) {
			t.Errorf("runRowHtml missing card class %q (stacked card at ≤640px):\n%s", cls, row)
		}
	}
	// The previously-clipped `by` field must survive as a labelled card cell.
	if !strings.Contains(row, "'by'") && !strings.Contains(row, ">by<") {
		t.Errorf("runRowHtml drops the `by` field label on mobile (still clipped):\n%s", row)
	}

	// The injected stylesheet must define the utilities the markup relies on:
	// proves the committed tailwind.css was regenerated from the new classes, so
	// serveUI ships working CSS (dev/test parity, like the T1/T2 guards).
	if !strings.Contains(page, "display:table-header-group") {
		t.Errorf("injected stylesheet defines no .table-header-group utility (stale tailwind.css?)")
	}
}
