package server

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runStreamHarness drives the real attachStream from the embedded index.html
// through a fake EventSource + minimal DOM (mirrors ui_reconnect_test.go), runs
// `script` (which fires events via fire(kind, ev) and writes a result), and
// returns stdout. fire() drops events once the active EventSource is closed —
// modelling the browser: after es.close() no further SSE events arrive.
func runStreamHarness(t *testing.T, script string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping UI stream test")
	}
	uiPath, err := filepath.Abs("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	harness := `
const fs = require('fs');
const vm = require('vm');
const html = fs.readFileSync(process.argv[1], 'utf8');
const m = html.match(/<script>([\s\S]*?)<\/script>/);
if (!m) { console.error('no <script> found'); process.exit(2); }
const listeners = {};
const instances = [];
class FakeES {
  constructor(url) { this.url = url; this.closed = false; instances.push(this); }
  addEventListener(kind, fn) { (listeners[kind] = listeners[kind] || []).push(fn); }
  set onopen(fn) {} set onerror(fn) {}
  close() { this.closed = true; }
}
const KNOWN = ['run-review', 'run-failure', 'human-dock', 'run-blocked'];
const els = {};
const mkEl = () => ({ innerHTML: '', textContent: '' });
const sandbox = {
  window: { addEventListener() {} },
  document: { getElementById: id => KNOWN.includes(id) ? (els[id] = els[id] || mkEl()) : null },
  location: {}, console, EventSource: FakeES,
  fetch: () => Promise.resolve({ ok: false, json: () => Promise.resolve({}) }),
};
vm.createContext(sandbox);
vm.runInContext(m[1], sandbox);
// fire mirrors SSE delivery, but stops once the active stream is closed — a
// closed EventSource receives no more events (so a premature close masks them).
const fire = (kind, ev) => {
  const last = instances[instances.length - 1];
  if (last && last.closed) return;
  (listeners[kind] || []).forEach(fn => fn({ data: JSON.stringify(ev) }));
};
const active = () => instances[instances.length - 1];
sandbox.attachStream('run1');
` + script
	out, err := exec.Command("node", "-e", harness, uiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	return string(out)
}

// TestChildTerminalDoesNotCloseStream: a forwarded child terminal event
// (pipeline_completed OR pipeline_failed, detail.source="child") must NOT end
// the parent's SSE stream (ui-tailwind-spec T8b). Regression: an unguarded
// pipeline_completed handler fired done() on a child's completion, closing the
// EventSource and masking a later real PARENT pipeline_failed — the run froze
// at "resolved" while actually failing.
func TestChildTerminalDoesNotCloseStream(t *testing.T) {
	out := runStreamHarness(t, `
fire('pipeline_failed',   { message: 'findings: X', detail: { source: 'child' } });
fire('pipeline_completed',{ status: 'success',      detail: { source: 'child' } });
const closedAfterChild = active().closed;
fire('pipeline_failed',   { message: 'PARENTBOOM' });   // the run's real failure
process.stdout.write(JSON.stringify({
  closedAfterChild,
  fail: els['run-failure'].innerHTML,
  review: els['run-review'].innerHTML,
}));
`)
	if strings.Contains(out, `"closedAfterChild":true`) {
		t.Errorf("child terminal event closed the parent stream:\n%s", out)
	}
	if !strings.Contains(out, "PARENTBOOM") {
		t.Errorf("real parent failure was masked (stream closed early):\n%s", out)
	}
	if strings.Contains(out, "resolved") {
		t.Errorf("child completion prematurely marked notes resolved:\n%s", out)
	}
}

// TestParentCompletedMarksResolvedAndCloses: the PARENT run's own completion is
// terminal — it marks the review notes resolved and closes the stream (T8b).
func TestParentCompletedMarksResolvedAndCloses(t *testing.T) {
	out := runStreamHarness(t, `
fire('pipeline_failed',   { message: 'findings: X', detail: { source: 'child' } });
fire('pipeline_completed',{ status: 'success' });       // the parent's completion
process.stdout.write(JSON.stringify({
  closed: active().closed,
  review: els['run-review'].innerHTML,
}));
`)
	if !strings.Contains(out, `"closed":true`) {
		t.Errorf("parent completion did not close the stream:\n%s", out)
	}
	if !strings.Contains(out, "resolved") || !strings.Contains(out, "findings: X") {
		t.Errorf("parent completion did not mark findings resolved:\n%s", out)
	}
}

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
