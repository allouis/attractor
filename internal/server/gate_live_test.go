package server

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGateDockLiveFromStream drives the real attachStream (web-ui-v2-spec U5 —
// everything live): an interview_started event refreshes the wait.human dock
// immediately over SSE rather than waiting up to a poll interval, and an
// interview_answered/timeout event refreshes it again (the gate cleared, the
// run resumed). A plain stage event must NOT refresh the dock. Uses node's vm
// sandbox with a fake EventSource + a fetch spy, mirroring timeline_wiring_test.go.
func TestGateDockLiveFromStream(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping UI gate-live test")
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
let onopenCb = null;
class FakeES {
  constructor(url) { this.url = url; }
  addEventListener(kind, fn) { (listeners[kind] = listeners[kind] || []).push(fn); }
  set onopen(fn) { onopenCb = fn; }
  set onerror(fn) {}
  close() {}
}

const els = {};
const el = () => ({ innerHTML: '', value: '', textContent: '', scrollTop: 0, scrollHeight: 0,
  querySelectorAll: () => [], querySelector: () => null, firstElementChild: null,
  appendChild() {}, insertAdjacentHTML(pos, html) { this.innerHTML += html; },
  classList: { toggle() {}, add() {}, remove() {} }, setAttribute() {} });
const document = { getElementById: (id) => (els[id] = els[id] || el()),
  createElement: () => el() };

const fetched = [];
const fetch = (url) => { fetched.push(url); return Promise.resolve({ ok: true, status: 200,
  json: () => Promise.resolve({ questions: [] }) }); };

const sandbox = { window: { addEventListener() {} }, document, location: {},
  console, EventSource: FakeES, fetch };
vm.createContext(sandbox);
vm.runInContext(m[1] + '\nglobalThis.__setRun = (r) => { currentRun = r; };', sandbox);

const fire = (kind, ev) => (listeners[kind] || []).forEach(fn => fn({ data: JSON.stringify(ev) }));

sandbox.__setRun('run1');
sandbox.attachStream('run1');
if (onopenCb) onopenCb();
// A plain stage event must not touch the dock.
fire('stage_started',      { node_id: 'plan', ts: 't0' });
fire('interview_started',  { node_id: 'gate', question_id: 'q1', ts: 't1' });
fire('interview_answered', { node_id: 'gate', question_id: 'q1', ts: 't2' });

const questionsFetches = fetched.filter(u => u.indexOf('/questions') !== -1);
process.stdout.write(JSON.stringify({ questionsFetches }));
`
	out, err := exec.Command("node", "-e", harness, uiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	result := string(out)

	// Both gate events refresh the dock; the stage event does not — so exactly
	// two /questions fetches, both scoped to the run.
	if n := strings.Count(result, "/pipelines/run1/questions"); n != 2 {
		t.Errorf("expected 2 live dock refreshes (started + answered), got %d:\n%s", n, result)
	}
}
