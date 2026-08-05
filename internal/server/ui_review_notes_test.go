package server

import (
	"strings"
	"testing"
)

// TestChildFindingsRoutedToReview: a forwarded child pipeline_failed (a review
// verdict / findings, tagged detail.source="child") routes to the collapsible
// "Review notes" section, NOT the run's red failure banner (ui-tailwind-spec
// T8b). A manager_loop re-emits its child's pipeline_failed onto the parent
// stream, so a succeeded run replays one — it must not read as a failure.
func TestChildFindingsRoutedToReview(t *testing.T) {
	got := evalUI(t, `classifyPipelineFailed({message:"findings: X",detail:{source:"child"}}).section`)
	if got != "review" {
		t.Errorf("child findings should route to review, got %q", got)
	}
}

// TestGenuineFailureStaysErrorBanner: the run's own terminal failure (no
// child source) keeps the distinct red failure banner (ui-tailwind-spec T8b).
func TestGenuineFailureStaysErrorBanner(t *testing.T) {
	// No detail at all: a real parent failure.
	if got := evalUI(t, `classifyPipelineFailed({message:"boom"}).section`); got != "failure" {
		t.Errorf("parent failure should route to failure banner, got %q", got)
	}
	// A non-child source is still the run's failure, not review notes.
	if got := evalUI(t, `classifyPipelineFailed({message:"boom",detail:{source:"phone"}}).section`); got != "failure" {
		t.Errorf("non-child failure should route to failure banner, got %q", got)
	}
	// The failure path still renders the loud, error-red banner.
	if html := evalUI(t, `failureBannerHtml("boom")`); !strings.Contains(html, "banner error") {
		t.Errorf("genuine failure lost its error-red banner:\n%s", html)
	}
}

// TestReviewNotesCollapsibleNotError: the review-notes section is a labelled,
// collapsible block that is NOT styled as an error (ui-tailwind-spec T8b).
func TestReviewNotesCollapsibleNotError(t *testing.T) {
	html := evalUI(t, `reviewNotesHtml(["findings: X"], false)`)
	for _, want := range []string{"<details", "Review notes", "findings: X"} {
		if !strings.Contains(html, want) {
			t.Errorf("review notes missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "banner error") || strings.Contains(html, "state-failed") {
		t.Errorf("review notes must not read as an error:\n%s", html)
	}
}

// TestReviewNotesResolvedMarked: on a succeeded run the findings were fixed by
// the loop, so the section is marked resolved (ui-tailwind-spec T8b).
func TestReviewNotesResolvedMarked(t *testing.T) {
	html := evalUI(t, `reviewNotesHtml(["findings: X"], true)`)
	if !strings.Contains(strings.ToLower(html), "resolved") {
		t.Errorf("resolved review notes should be marked resolved:\n%s", html)
	}
	if strings.Contains(html, "banner error") {
		t.Errorf("resolved review notes must not read as an error:\n%s", html)
	}
}

// TestReviewNotesEmpty: no findings → no section (a healthy run shows nothing).
func TestReviewNotesEmpty(t *testing.T) {
	if html := evalUI(t, `reviewNotesHtml([], false)`); strings.TrimSpace(html) != "" {
		t.Errorf("empty review notes should render nothing, got:\n%s", html)
	}
}

// TestReviewNotesEscaped: the verdict text is attacker-controlled and must be
// escaped (XSS parity with failureBannerHtml).
func TestReviewNotesEscaped(t *testing.T) {
	html := evalUI(t, `reviewNotesHtml(["<img src=x onerror=1>"], false)`)
	if strings.Contains(html, "<img") {
		t.Errorf("review notes did not escape findings text:\n%s", html)
	}
}
