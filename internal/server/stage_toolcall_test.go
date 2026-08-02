package server

import (
	"strings"
	"testing"
)

// TestToolCallHtml drives the UI's per-tool-call renderer (web-ui-v2-spec U2 —
// the inspector lists a stage's tool calls inline). It surfaces the tool name
// as the summary and pretty-prints the raw payload; every field is escaped.
func TestToolCallHtml(t *testing.T) {
	html := evalUI(t, `toolCallHtml({tool_name:'Bash', tool_input:{command:'ls -la'}})`)
	if !strings.Contains(html, "Bash") {
		t.Errorf("tool-call summary missing the tool name:\n%s", html)
	}
	if !strings.Contains(html, "ls -la") {
		t.Errorf("tool-call body missing the payload:\n%s", html)
	}

	// The hook name is the fallback label when no tool_name is present.
	fb := evalUI(t, `toolCallHtml({hook_name:'post_tool'})`)
	if !strings.Contains(fb, "post_tool") {
		t.Errorf("tool-call missing its fallback label:\n%s", fb)
	}

	// A malicious tool name / payload must render inert.
	esc := evalUI(t, `toolCallHtml({tool_name:'<img src=x onerror=alert(1)>', tool_input:'</pre><script>alert(2)</script>'})`)
	if strings.Contains(esc, "<img src=x onerror=") || strings.Contains(esc, "<script>alert(2)</script>") {
		t.Errorf("tool call rendered attacker markup live:\n%s", esc)
	}
}
