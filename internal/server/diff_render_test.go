package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// jsStr encodes s as a JS/JSON string literal (quotes included) so a Go test
// can splice arbitrary text — newlines, quotes, markup — into a UI expression
// for evalUI without hand-escaping.
func jsStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestDiffHtml drives the U4 unified-diff renderer (web-ui-v2-spec U4): added
// lines carry an add class, removed lines a del class, hunk headers a hunk
// class, and context lines render plain. Every line is attacker-controlled
// (a diff body can contain arbitrary source) and must render inert.
func TestDiffHtml(t *testing.T) {
	diff := "@@ -1,2 +1,2 @@\n-old line\n+new line\n unchanged\n"
	html := evalUI(t, "diffHtml("+jsStr(diff)+")")

	for _, want := range []string{"diff-add", "diff-del", "diff-hunk", "new line", "old line", "unchanged"} {
		if !strings.Contains(html, want) {
			t.Errorf("diff html missing %q:\n%s", want, html)
		}
	}

	// A file-header line (+++/---) is not mistaken for an add/remove of source.
	hdr := evalUI(t, "diffHtml("+jsStr("--- a/x\n+++ b/x\n")+")")
	if strings.Contains(hdr, "diff-add\">+++") || strings.Contains(hdr, "diff-del\">---") {
		t.Errorf("+++/--- headers should not render as add/del lines:\n%s", hdr)
	}

	// Empty / whitespace-only input renders nothing (a run that produced no diff).
	if got := evalUI(t, "diffHtml('')"); strings.TrimSpace(got) != "" {
		t.Errorf("empty diff should render empty, got:\n%s", got)
	}

	// Attacker markup in a diff body stays inert text.
	esc := evalUI(t, "diffHtml("+jsStr("+<img src=x onerror=alert(1)>\n")+")")
	if strings.Contains(esc, "<img src=x onerror=") {
		t.Errorf("diff rendered attacker markup live:\n%s", esc)
	}
}
