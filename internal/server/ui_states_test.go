package server

import (
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
