package server

import (
	"strings"
	"testing"
)

// TestStageDetailHtml drives the U2 inspector panel builder (web-ui-v2-spec
// U2): given a stage's detail (status/prompt/response/tool_calls) and its
// timing, it renders the status, elapsed time, prompt, response and each tool
// call, and preserves the live-tail #node-log container seeded with prior
// chunks. Every stage field is escaped.
func TestStageDetailHtml(t *testing.T) {
	html := evalUI(t, `stageDetailHtml('plan',`+
		`{status:'success', prompt:'do the plan', response:'planned it', tool_calls:[{tool_name:'Bash'}]},`+
		`{start:'2026-08-02T10:00:00Z', end:'2026-08-02T10:00:02Z'}, 'LIVELOG')`)

	for _, want := range []string{"plan", "success", "do the plan", "planned it", "Bash", "2.0s", "LIVELOG"} {
		if !strings.Contains(html, want) {
			t.Errorf("stage detail missing %q:\n%s", want, html)
		}
	}
	if !strings.Contains(html, `id="node-log"`) {
		t.Errorf("stage detail dropped the live-tail container:\n%s", html)
	}

	// A stage with no status.json yet reads as running, not blank.
	running := evalUI(t, `stageDetailHtml('plan', {status:'', prompt:'', response:'', tool_calls:[]}, {start:'2026-08-02T10:00:00Z', end:''}, '')`)
	if !strings.Contains(running, "running") {
		t.Errorf("in-flight stage should read as running:\n%s", running)
	}

	// Attacker-controlled prompt / response / status must render inert.
	esc := evalUI(t, `stageDetailHtml('<b>x</b>', {status:'<i>s</i>', prompt:'<img src=x onerror=alert(1)>', response:'</pre><script>alert(2)</script>', tool_calls:[]}, {}, '')`)
	for _, bad := range []string{"<img src=x onerror=", "<script>alert(2)</script>", "<b>x</b>", "<i>s</i>"} {
		if strings.Contains(esc, bad) {
			t.Errorf("stage detail rendered attacker markup live (%q):\n%s", bad, esc)
		}
	}
}
