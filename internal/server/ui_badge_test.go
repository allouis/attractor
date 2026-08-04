package server

import (
	"strings"
	"testing"
)

// runStates are the six run states the fleet renders; stateBadge must colour
// each one (ui-tailwind-spec T6a — one badge per run state, theme-aware).
var runStates = []string{"queued", "running", "retrying", "completed", "failed", "cancelled"}

// TestStateBadge: the shared run-state pill adopts the Tailwind Plus badge
// shape (rounded, inset ring) while carrying its per-state colour through the
// existing `.status.<state>` token rule — so light/dark tracks and no stock
// gray/indigo colour leaks in (ui-tailwind-spec T6a).
func TestStateBadge(t *testing.T) {
	for _, state := range runStates {
		html := evalUI(t, `stateBadge('`+state+`')`)
		// Tailwind Plus badge shape.
		for _, want := range []string{"inline-flex", "rounded-md", "ring-1", "ring-inset"} {
			if !strings.Contains(html, want) {
				t.Errorf("stateBadge(%q) missing badge shape %q:\n%s", state, want, html)
			}
		}
		// Per-state colour rides the existing status token class.
		if !strings.Contains(html, "status "+state) {
			t.Errorf("stateBadge(%q) missing the state colour class:\n%s", state, html)
		}
		// Labelled with the state word.
		if !strings.Contains(html, state) {
			t.Errorf("stateBadge(%q) missing its label:\n%s", state, html)
		}
		// Theme-aware: no hardcoded stock palette.
		for _, bad := range []string{"gray-", "indigo-", "#"} {
			if strings.Contains(html, bad) {
				t.Errorf("stateBadge(%q) leaked a hardcoded colour %q:\n%s", state, bad, html)
			}
		}
	}
}

// TestStateBadgeLink: given an href the badge renders as a link (the Items view
// tints a run-id link by its run state) with a custom label, still a badge.
func TestStateBadgeLink(t *testing.T) {
	html := evalUI(t, `stateBadge('running', 'abc12345', '#run/abc12345')`)
	if !strings.Contains(html, `<a `) || !strings.Contains(html, `href="#run/abc12345"`) {
		t.Errorf("linked badge is not an anchor to the run:\n%s", html)
	}
	if !strings.Contains(html, "abc12345") {
		t.Errorf("linked badge missing its label:\n%s", html)
	}
	if !strings.Contains(html, "rounded-md") {
		t.Errorf("linked badge lost the badge shape:\n%s", html)
	}
}

// TestStateBadgeEscapes: status, label and href are attacker-controlled and
// must not render as live markup (mirrors the UI's other XSS guards).
func TestStateBadgeEscapes(t *testing.T) {
	esc := evalUI(t, `stateBadge('<img src=x onerror=alert(1)>', '</a><script>alert(2)</script>', 'javascript:x"><b>')`)
	for _, bad := range []string{"<img src=x onerror=", "<script>alert(2)</script>", `"><b>`} {
		if strings.Contains(esc, bad) {
			t.Errorf("stateBadge rendered attacker input as markup (%q):\n%s", bad, esc)
		}
	}
}
