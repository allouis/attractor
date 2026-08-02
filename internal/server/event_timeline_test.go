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
