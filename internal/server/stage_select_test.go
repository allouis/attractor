package server

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSelectNodeFetchesStageDetail drives the real selectNode from the embedded
// index.html (web-ui-v2-spec U2): clicking a node fetches GET …/stages/{node}
// and renders the inspector, and the node's timing — captured from the
// stage_started/stage_completed events — shows as elapsed time. Uses node's vm
// sandbox with a fake EventSource, fetch and document, mirroring
// ui_reconnect_test.go.
func TestSelectNodeFetchesStageDetail(t *testing.T) {
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

const stageDetail = { status: 'success', prompt: 'do the plan',
  response: 'planned it', tool_calls: [{ tool_name: 'Bash' }] };
const fetch = (url) => Promise.resolve({ ok: true, status: 200,
  json: () => Promise.resolve(stageDetail) });

const sandbox = { window: { addEventListener() {} }, document, location: {},
  console, EventSource: FakeES, fetch };
vm.createContext(sandbox);
vm.runInContext(m[1] + '\nglobalThis.__setRun = (r) => { currentRun = r; };', sandbox);

const fire = (kind, ev) => (listeners[kind] || []).forEach(fn => fn({ data: JSON.stringify(ev) }));
sandbox.__setRun('run1');
sandbox.attachStream('run1');
if (onopenCb) onopenCb();
fire('stage_started',   { node_id: 'n1', ts: '2026-08-02T10:00:00Z' });
fire('stage_completed', { node_id: 'n1', ts: '2026-08-02T10:00:02Z' });

sandbox.selectNode('n1').then(() => {
  process.stdout.write(els['node-pane'].innerHTML);
}).catch(e => { console.error(e); process.exit(3); });
`
	out, err := exec.Command("node", "-e", harness, uiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	pane := string(out)
	for _, want := range []string{"success", "do the plan", "planned it", "Bash", "2.0s"} {
		if !strings.Contains(pane, want) {
			t.Errorf("inspector missing %q after selectNode:\n%s", want, pane)
		}
	}
}
