package server

import (
	"strings"
	"testing"
)

// TestBlockedBannerHtml: a blocked run surfaces a loud banner at the run-detail
// header (web-ui-v2-spec U5 — a blocked run is visually loud), distinct from
// the U3 failure banner. No open questions → no banner. It announces how many
// answers the run is waiting on and reads as an alert.
func TestBlockedBannerHtml(t *testing.T) {
	if got := evalUI(t, `blockedBannerHtml([])`); strings.TrimSpace(got) != "" {
		t.Errorf("no open questions should yield no banner, got %q", got)
	}

	one := evalUI(t, `blockedBannerHtml([{id:'q1', text:'Approve?'}])`)
	if !strings.Contains(one, "banner blocked") {
		t.Errorf("blocked banner missing its class:\n%s", one)
	}
	if !strings.Contains(one, `role="alert"`) {
		t.Errorf("blocked banner should announce itself to assistive tech:\n%s", one)
	}
	if !strings.Contains(one, "1") {
		t.Errorf("blocked banner should report the pending count:\n%s", one)
	}

	two := evalUI(t, `blockedBannerHtml([{id:'q1'},{id:'q2'}])`)
	if !strings.Contains(two, "2") {
		t.Errorf("blocked banner should count both questions:\n%s", two)
	}
}

// TestBlockedBannerEscapes: any question text carried into the banner is inert
// (guards against stored XSS through a wait.human prompt).
func TestBlockedBannerEscapes(t *testing.T) {
	got := evalUI(t, `blockedBannerHtml([{id:'q1', text:'<img src=x onerror=alert(1)>'}])`)
	if strings.Contains(got, "<img src=x onerror=") {
		t.Errorf("blocked banner rendered question text as live markup:\n%s", got)
	}
}
