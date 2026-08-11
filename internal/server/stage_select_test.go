package server

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runSelectHarness drives the real selectNode from the embedded index.html
// through a fake EventSource + a URL-routing fetch, then returns the rendered
// #node-pane. `route` is a JS expression: a function (url) => body that maps
// each endpoint (/state, /nodes, /stages, /tail) to its JSON/text payload, so
// the R3 inspector's three hydrate sources can be mocked independently.
func runSelectHarness(t *testing.T, route string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping UI select test")
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
const el = () => ({ innerHTML: '', value: '', scrollTop: 0, scrollHeight: 0,
  querySelectorAll: () => [], querySelector: () => null, firstElementChild: null,
  insertAdjacentHTML(pos, html) { this.innerHTML += html; },
  classList: { toggle() {}, add() {}, remove() {} }, setAttribute() {} });
const document = { getElementById: (id) => (els[id] = els[id] || el()) };

const route = (` + route + `);
const fetch = (url) => {
  const body = route(url);
  const isText = typeof body === 'string';
  return Promise.resolve({ ok: true, status: 200,
    json: () => Promise.resolve(isText ? {} : body),
    text: () => Promise.resolve(isText ? body : '') });
};

const sandbox = { window: { addEventListener() {} }, document, location: {},
  console, EventSource: FakeES, fetch, setTimeout, clearTimeout };
vm.createContext(sandbox);
vm.runInContext(m[1] + '\nglobalThis.__setRun = (r) => { currentRun = r; };', sandbox);

const fire = (kind, ev) => (listeners[kind] || []).forEach(fn => fn({ data: JSON.stringify(ev) }));
sandbox.__setRun('run1');
sandbox.attachStream('run1');
if (onopenCb) onopenCb();

sandbox.selectNode('n1').then(() => {
  process.stdout.write(els['node-pane'].innerHTML);
}).catch(e => { console.error(e); process.exit(3); });
`
	out, err := exec.Command("node", "-e", harness, uiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	return string(out)
}

// TestSelectNodeHydratesCodergen: selecting a completed codergen node hydrates
// the R3 inspector from its three server sources — status + timing from /state,
// the resolved prompt from /nodes, the response from /stages — never from the
// replay-derived accumulators.
func TestSelectNodeHydratesCodergen(t *testing.T) {
	pane := runSelectHarness(t, `(url) => {
	  if (url.indexOf('/state') !== -1) return { nodes: { n1: { status:'completed', attempts:1,
	    started_at:'2026-08-02T10:00:00Z', completed_at:'2026-08-02T10:00:02Z', outcome:'success' } } };
	  if (url.indexOf('/nodes/') !== -1) return { id:'n1', type:'codergen', prompt:'do the plan' };
	  if (url.indexOf('/stages/') !== -1) return { status:'success', response:'planned it' };
	  return {};
	}`)
	for _, want := range []string{"completed", "2.0s", "do the plan", "planned it", "Response"} {
		if !strings.Contains(pane, want) {
			t.Errorf("inspector missing %q after selectNode:\n%s", want, pane)
		}
	}
	if strings.Contains(pane, "unavailable") {
		t.Errorf("a completed node must never render as unavailable:\n%s", pane)
	}
}

// TestSelectNodeToolShowsExitAndOutput: selecting a failed tool node shows its
// exit code (from the stage detail) and its stdout/stderr (from the tail
// endpoint, ANSI-rendered).
func TestSelectNodeToolShowsExitAndOutput(t *testing.T) {
	pane := runSelectHarness(t, `(url) => {
	  if (url.indexOf('/state') !== -1) return { nodes: { n1: { status:'failed', attempts:1,
	    started_at:'2026-08-02T10:00:00Z', completed_at:'2026-08-02T10:00:03Z', outcome:'fail' } } };
	  if (url.indexOf('/nodes/') !== -1) return { id:'n1', type:'tool', prompt:'' };
	  if (url.indexOf('/tail') !== -1) return (url.indexOf('file=stdout') !== -1) ? 'BUILD \u001b[31mFAILED\u001b[0m' : '';
	  if (url.indexOf('/stages/') !== -1) return { status:'fail', exit_code:'2' };
	  return {};
	}`)
	for _, want := range []string{"failed", "exit 2", "Output", "FAILED", "ansi-red"} {
		if !strings.Contains(pane, want) {
			t.Errorf("tool inspector missing %q after selectNode:\n%s", want, pane)
		}
	}
	if strings.Contains(pane, "\x1b[") {
		t.Errorf("raw ANSI escape leaked into the tool inspector:\n%s", pane)
	}
}
