package server

import (
	"strings"
	"testing"
)

// TestFailureBannerHtml drives the run-view failure banner: it surfaces why the
// run failed (the first thing a debugger needs), renders nothing for a healthy
// run, and escapes the attacker-controlled reason.
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
