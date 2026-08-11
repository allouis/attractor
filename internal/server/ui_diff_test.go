package server

import (
	"strings"
	"testing"
)

// The Diff section is honest about WHY it is empty (ui-run-view-v3 R4): the
// daemon's X-Diff-Status header (P2) distinguishes a computable-but-empty diff
// ("no commits yet") from an uncomputable one ("not computable") from a real
// change. diffBodyHtml is the pure renderer the diff loader calls with the
// header value and the body text.

// A run before its first commit reads "no commits yet", never the old
// misleading "produced no diff".
func TestDiffBodyNoCommitsYet(t *testing.T) {
	html := evalUI(t, `diffBodyHtml('no-commits-yet', '')`)
	if !strings.Contains(strings.ToLower(html), "no commits yet") {
		t.Errorf("no-commits-yet must say so:\n%s", html)
	}
}

// A run whose diff can't be computed (no jj range, workspace gone) says so
// plainly rather than pretending nothing changed.
func TestDiffBodyNotComputable(t *testing.T) {
	html := evalUI(t, `diffBodyHtml('not-computable', '')`)
	if !strings.Contains(strings.ToLower(html), "not computable") {
		t.Errorf("not-computable must say so:\n%s", html)
	}
	if strings.Contains(strings.ToLower(html), "no commits") {
		t.Errorf("not-computable must not be confused with no-commits-yet:\n%s", html)
	}
}

// A real change renders line-by-line, coloured.
func TestDiffBodyOK(t *testing.T) {
	html := evalUI(t, "diffBodyHtml('ok', '+added line\\n-removed line')")
	for _, want := range []string{"diff-add", "diff-del", "added line", "removed line"} {
		if !strings.Contains(html, want) {
			t.Errorf("ok diff missing %q:\n%s", want, html)
		}
	}
}

// The diff body is attacker-controlled source; every line is escaped inert.
func TestDiffBodyEscapes(t *testing.T) {
	html := evalUI(t, "diffBodyHtml('ok', '+<img src=x onerror=alert(1)>')")
	if strings.Contains(html, "<img src=x") {
		t.Errorf("diff body leaked attacker markup:\n%s", html)
	}
}
