package server

import (
	"strings"
	"testing"
)

// TestTimelineRowHtml drives the U3 timeline row builder (web-ui-v2-spec U3):
// one event renders its kind, node id and message, and every attacker-
// controlled field is escaped inert.
func TestTimelineRowHtml(t *testing.T) {
	row := evalUI(t, `timelineRowHtml('stage_failed',`+
		`{node_id:'plan', message:'boom', ts:'2026-08-02T10:00:00Z'})`)

	for _, want := range []string{"stage_failed", "plan", "boom"} {
		if !strings.Contains(row, want) {
			t.Errorf("timeline row missing %q:\n%s", want, row)
		}
	}

	// A pipeline-level event carries no node; the row still renders.
	noNode := evalUI(t, `timelineRowHtml('pipeline_started', {ts:'2026-08-02T10:00:00Z'})`)
	if !strings.Contains(noNode, "pipeline_started") {
		t.Errorf("node-less event should still render its kind:\n%s", noNode)
	}

	// Attacker-controlled kind / node / message must render inert.
	esc := evalUI(t, `timelineRowHtml('<b>k</b>',`+
		`{node_id:'<i>n</i>', message:'<img src=x onerror=alert(1)>', ts:''})`)
	for _, bad := range []string{"<b>k</b>", "<i>n</i>", "<img src=x onerror="} {
		if strings.Contains(esc, bad) {
			t.Errorf("timeline row rendered attacker markup live (%q):\n%s", bad, esc)
		}
	}
}

// TestFailureBannerHtml drives the U3 run-header failure banner: a reason
// renders prominently, an empty reason renders nothing (a healthy run shows no
// banner), and the reason is escaped.
func TestFailureBannerHtml(t *testing.T) {
	shown := evalUI(t, `failureBannerHtml('checks failed: lint')`)
	if !strings.Contains(shown, "checks failed: lint") {
		t.Errorf("failure banner missing the reason:\n%s", shown)
	}

	if empty := evalUI(t, `failureBannerHtml('')`); strings.TrimSpace(empty) != "" {
		t.Errorf("no failure reason should render an empty banner, got:\n%s", empty)
	}

	esc := evalUI(t, `failureBannerHtml('<img src=x onerror=alert(1)>')`)
	if strings.Contains(esc, "<img src=x onerror=") {
		t.Errorf("failure banner rendered attacker markup live:\n%s", esc)
	}
}
