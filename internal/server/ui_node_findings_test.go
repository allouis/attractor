package server

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runStreamHarness drives the real attachStream from the embedded index.html
// through a fake EventSource + minimal DOM (mirrors ui_reconnect_test.go), runs
// `script` (which fires events via fire(kind, ev), may read context globals via
// vm.runInContext(expr, sandbox), and writes a result), and returns stdout.
// fire() drops events once the active EventSource is closed — modelling the
// browser: after es.close() no further SSE events arrive.
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
const KNOWN = ['run-failure', 'human-dock', 'run-blocked', 'graph', 'node-pane', 'node-log'];
const els = {};
const mkEl = () => ({ innerHTML: '', textContent: '', querySelectorAll: () => [], classList: { toggle() {} } });
const sandbox = {
  window: { addEventListener() {} },
  document: { getElementById: id => KNOWN.includes(id) ? (els[id] = els[id] || mkEl()) : null },
  location: {}, console, EventSource: FakeES, setTimeout, clearTimeout,
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
// read a context global (e.g. nodeFindings) — top-level let/const are not
// sandbox properties, but a second runInContext sees the shared lexical scope.
const read = expr => vm.runInContext(expr, sandbox);
sandbox.attachStream('run1');
` + script
	out, err := exec.Command("node", "-e", harness, uiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	return string(out)
}

// TestChildFindingsAttributedToActiveNode: a forwarded child pipeline_failed
// (a review verdict / findings, tagged detail.source="child") is that node's
// output — it must attach to the parent node that ran the child pipeline (the
// active non-child stage), NOT the run's failure banner, and NOT close the
// stream (ui-tailwind-spec T9b). The active parent is the last non-child
// stage_started; here review_loop.
func TestChildFindingsAttributedToActiveNode(t *testing.T) {
	out := runStreamHarness(t, `
fire('stage_started', { node_id: 'review_loop', ts: 1 });
fire('pipeline_failed', { message: 'findings: X', detail: { source: 'child' } });
process.stdout.write(JSON.stringify({
  findings: read('JSON.stringify(nodeFindings["review_loop"] || [])'),
  active: read('activeParentNode'),
  closed: active().closed,
  fail: (els['run-failure'] && els['run-failure'].innerHTML) || '',
}));
`)
	if !strings.Contains(out, "findings: X") {
		t.Errorf("child findings not attributed to the active node:\n%s", out)
	}
	if !strings.Contains(out, `"active":"review_loop"`) {
		t.Errorf("active parent node not tracked from stage_started:\n%s", out)
	}
	if strings.Contains(out, `"closed":true`) {
		t.Errorf("a forwarded child failure closed the parent stream:\n%s", out)
	}
	if strings.Contains(out, `"fail":"`) && !strings.Contains(out, `"fail":""`) {
		t.Errorf("child findings leaked into the run failure banner:\n%s", out)
	}
}

// TestGenuineFailureStillClosesWithBanner: the run's own terminal failure (no
// child source) keeps the distinct red failure banner and closes the stream
// (ui-tailwind-spec T9b — the run's OWN failure stays the red banner).
func TestGenuineFailureStillClosesWithBanner(t *testing.T) {
	out := runStreamHarness(t, `
fire('stage_started', { node_id: 'review_loop', ts: 1 });
fire('pipeline_failed', { message: 'findings: X', detail: { source: 'child' } });
const closedAfterChild = active().closed;
fire('pipeline_failed', { message: 'PARENTBOOM' });   // the run's real failure
process.stdout.write(JSON.stringify({
  closedAfterChild,
  closed: active().closed,
  fail: els['run-failure'].innerHTML,
}));
`)
	if strings.Contains(out, `"closedAfterChild":true`) {
		t.Errorf("child failure closed the parent stream early:\n%s", out)
	}
	if !strings.Contains(out, "PARENTBOOM") || !strings.Contains(out, "banner error") {
		t.Errorf("real parent failure did not render the red banner:\n%s", out)
	}
	if !strings.Contains(out, `"closed":true`) {
		t.Errorf("the run's own failure did not close the stream:\n%s", out)
	}
}

// TestNodeFindingsHtml: nodeFindingsHtml renders a node's child-pipeline
// findings as a labelled, collapsible block — NOT error-red (T9b). On a
// resolved (completed) node the findings were fixed by the loop, so it is
// marked resolved.
func TestNodeFindingsHtml(t *testing.T) {
	html := evalUI(t, `nodeFindingsHtml(["finding X"], false)`)
	for _, want := range []string{"<details", "Findings", "finding X"} {
		if !strings.Contains(html, want) {
			t.Errorf("node findings missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "banner error") || strings.Contains(html, "state-failed") {
		t.Errorf("node findings must not read as an error:\n%s", html)
	}
	if r := evalUI(t, `nodeFindingsHtml(["finding X"], true)`); !strings.Contains(strings.ToLower(r), "resolved") {
		t.Errorf("resolved findings should be marked resolved:\n%s", r)
	}
	if e := evalUI(t, `nodeFindingsHtml([], false)`); strings.TrimSpace(e) != "" {
		t.Errorf("empty findings should render nothing, got:\n%s", e)
	}
	if x := evalUI(t, `nodeFindingsHtml(["<img src=x onerror=1>"], false)`); strings.Contains(x, "<img") {
		t.Errorf("node findings did not escape findings text:\n%s", x)
	}
}

// TestNodeInspectorRendersFindings: the node inspector (stageDetailHtml)
// surfaces the node's attributed findings inline (T9b — click the node → see
// its child's verdict/findings). A node with no findings shows no leftover
// section.
func TestNodeInspectorRendersFindings(t *testing.T) {
	html := evalUI(t, `stageDetailHtml("review_loop", {status:"completed"}, {}, "", ["finding X"], true)`)
	for _, want := range []string{"finding X", "Findings", "resolved"} {
		if !strings.Contains(html, want) {
			t.Errorf("node inspector missing %q:\n%s", want, html)
		}
	}
	empty := evalUI(t, `stageDetailHtml("plan", {status:"running"}, {}, "", [], false)`)
	if strings.Contains(empty, "Findings") {
		t.Errorf("a node with no findings should show no findings section:\n%s", empty)
	}
}

// TestReviewNotesSectionRemoved: the dedicated top-level Review-notes section
// and the classifyPipelineFailed "review" case are gone (T9b) — findings now
// live in the node inspector, not a special section baked with build-review
// semantics.
func TestReviewNotesSectionRemoved(t *testing.T) {
	for _, fn := range []string{"classifyPipelineFailed", "reviewNotesHtml"} {
		if got := evalUI(t, `typeof `+fn); got != "undefined" {
			t.Errorf("%s should be removed, got typeof %q", fn, got)
		}
	}
	uiPath, _ := filepath.Abs("ui/index.html")
	if b, err := exec.Command("grep", "-c", "run-review", uiPath).CombinedOutput(); err == nil {
		t.Errorf("index.html still references run-review:\n%s", b)
	}
}
