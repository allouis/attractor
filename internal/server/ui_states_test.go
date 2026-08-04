package server

import (
	"os"
	"strings"
	"testing"
)

// TestEmptyState: the shared empty-result placeholder carries the state-empty
// class and its message, and escapes it (web-ui-v2-spec U1 — one visual
// language for empty/loading/error). Asserting class AND text together means
// a stray comment cannot satisfy the grep.
func TestEmptyState(t *testing.T) {
	html := evalUI(t, `emptyState('no runs yet')`)
	if !strings.Contains(html, "state-empty") {
		t.Errorf("empty state missing its class:\n%s", html)
	}
	if !strings.Contains(html, "no runs yet") {
		t.Errorf("empty state missing its message:\n%s", html)
	}
	esc := evalUI(t, `emptyState('<img src=x onerror=alert(1)>')`)
	if strings.Contains(esc, "<img src=x onerror=") {
		t.Errorf("empty state rendered a message as live markup:\n%s", esc)
	}
}

// TestLoadingState: the in-flight placeholder is visually distinct (its own
// state-loading class), announces itself as busy, and defaults its text.
func TestLoadingState(t *testing.T) {
	html := evalUI(t, `loadingState()`)
	for _, want := range []string{"state-loading", "aria-busy", "loading"} {
		if !strings.Contains(html, want) {
			t.Errorf("loading state missing %q:\n%s", want, html)
		}
	}
}

// TestErrorState: a failure reads differently from an empty result — its own
// state-error class (not state-empty), an alert role, and an escaped message.
func TestErrorState(t *testing.T) {
	html := evalUI(t, `errorState('server unreachable')`)
	if !strings.Contains(html, "state-error") {
		t.Errorf("error state missing its class:\n%s", html)
	}
	if strings.Contains(html, "state-empty") {
		t.Errorf("error state must not read as an empty state:\n%s", html)
	}
	if !strings.Contains(html, "server unreachable") {
		t.Errorf("error state missing its message:\n%s", html)
	}
	if !strings.Contains(html, "role=\"alert\"") {
		t.Errorf("error state should announce itself to assistive tech:\n%s", html)
	}
	esc := evalUI(t, `errorState('<script>alert(2)</script>')`)
	if strings.Contains(esc, "<script>alert(2)</script>") {
		t.Errorf("error state rendered a message as live markup:\n%s", esc)
	}
}

// TestEmptyPanel: the primary empty views (Items, Runs) adopt the Tailwind
// Plus empty-state (empty-state.html) — a centred icon, heading and subtext —
// remapped to the UI tokens so light/dark tracks and no stock gray/indigo
// leaks in (ui-tailwind-spec T6a). Both strings are escaped.
func TestEmptyPanel(t *testing.T) {
	html := evalUI(t, `emptyPanel('No runs yet', 'Dispatch a workflow to see runs.')`)
	for _, want := range []string{"<svg", "No runs yet", "Dispatch a workflow to see runs.", "text-ink", "text-muted", "text-center"} {
		if !strings.Contains(html, want) {
			t.Errorf("empty panel missing %q:\n%s", want, html)
		}
	}
	for _, bad := range []string{"gray-", "indigo-", "#"} {
		if strings.Contains(html, bad) {
			t.Errorf("empty panel leaked a hardcoded colour %q:\n%s", bad, html)
		}
	}
	esc := evalUI(t, `emptyPanel('<img src=x onerror=alert(1)>', '</h3><script>alert(2)</script>')`)
	for _, bad := range []string{"<img src=x onerror=", "<script>alert(2)</script>"} {
		if strings.Contains(esc, bad) {
			t.Errorf("empty panel rendered attacker input as markup (%q):\n%s", bad, esc)
		}
	}
}

// TestEmptyPanelWired: the Items and Runs lists render through emptyPanel, not
// the bare one-line placeholder (ui-tailwind-spec T6a — those two views adopt
// the component; minor placeholders keep emptyState).
func TestEmptyPanelWired(t *testing.T) {
	src, err := os.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, fn := range []string{"renderItems", "renderFleet"} {
		if !strings.Contains(funcSource(t, string(src), fn), "emptyPanel(") {
			t.Errorf("%s does not use emptyPanel for its empty view", fn)
		}
	}
}
