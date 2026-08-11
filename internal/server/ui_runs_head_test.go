package server

import (
	"strings"
	"testing"
)

// The runs INDEX adopts the R1 header-card treatment per row (ui-run-view-v3
// R4): each row leads with the item identifier + title (identifier linked to
// the tracker), then the status pill, repo, and elapsed — the same identity a
// run's own header card shows, sourced from the run summary the list already
// carries (vars + started/completed).

// A run with an item leads with identifier · title, links the identifier to the
// tracker, and carries the status pill, repo, and elapsed.
func TestRunsRowHeaderCard(t *testing.T) {
	html := evalUI(t, `runsTableHtml([{
	  id:"deadbeefcafe1234", status:"failed",
	  started_at:"2026-08-02T10:00:00Z", completed_at:"2026-08-02T10:12:20Z",
	  repo:"TryGhost/Ghost", workflow_name:"implement",
	  item_ref:"linear:issue:uuid",
	  vars:{identifier:"HKG-1914", title:"Spike the jobs backend", url:"https://linear.app/x"}
	}])`)
	for _, want := range []string{
		"HKG-1914", "Spike the jobs backend", // identifier · title
		`href="https://linear.app/x"`, // identifier linked to the tracker
		"TryGhost/Ghost",              // repo
		"12m 20s",                     // elapsed (completed − started)
		"status failed",               // status pill
	} {
		if !strings.Contains(html, want) {
			t.Errorf("runs row missing header-card field %q:\n%s", want, html)
		}
	}
}

// A run with no item still names itself: the row falls back to the short run id
// (and the workflow), never rendering an empty identity.
func TestRunsRowNoItemFallsBackToId(t *testing.T) {
	html := evalUI(t, `runsTableHtml([{id:"deadbeefcafe1234",status:"running",started_at:"2026-07-25",workflow_name:"ship-it"}])`)
	for _, want := range []string{"deadbeef", "ship-it"} {
		if !strings.Contains(html, want) {
			t.Errorf("item-less run row missing %q:\n%s", want, html)
		}
	}
}

// The item identifier/title come from launch vars (attacker-controlled) and
// must render inert in the row.
func TestRunsRowItemEscapes(t *testing.T) {
	html := evalUI(t, `runsTableHtml([{id:"deadbeefcafe1234",status:"running",started_at:"2026-07-25",
	  vars:{identifier:"<img src=x onerror=alert(1)>", title:"<script>alert(2)</script>", url:"javascript:alert(3)"}}])`)
	for _, bad := range []string{"<img src=x onerror=", "<script>alert(2)</script>"} {
		if strings.Contains(html, bad) {
			t.Errorf("runs row leaked attacker markup from vars:\n%s", html)
		}
	}
}
