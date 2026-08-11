package server

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestReconnectDoesNotDuplicateNodeLogs simulates a mid-run SSE reconnect:
// the server replays the full history again, and the UI must not re-append
// node-log chunks it already applied. Drives the real attachStream from the
// embedded index.html via node's vm sandbox with a fake EventSource.
func TestReconnectDoesNotDuplicateNodeLogs(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping UI reconnect test")
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

const sandbox = {
  window: { addEventListener() {} },
  document: { getElementById: () => null },
  location: {},
  console,
  EventSource: FakeES,
  setTimeout, clearTimeout,
};
vm.createContext(sandbox);
// Expose the lexically-scoped nodeLog so the test can inspect it.
vm.runInContext(m[1] + '\nglobalThis.__nodeLog = nodeLog;', sandbox);

const fire = (kind, ev) => (listeners[kind] || []).forEach(fn => fn({ data: JSON.stringify(ev) }));
const history = [
  { kind: 'stage_progress', node_id: 'n1', detail: { kind: 'assistant_delta' }, message: 'hello ', ts: 't1' },
  { kind: 'stage_progress', node_id: 'n1', detail: { kind: 'assistant_delta' }, message: 'world',  ts: 't2' },
];

sandbox.attachStream('run1');
if (onopenCb) onopenCb();               // connection 1 opens
history.forEach(e => fire('stage_progress', e));   // live delivery
if (onopenCb) onopenCb();               // mid-run reconnect
history.forEach(e => fire('stage_progress', e));   // full history replayed

const log = sandbox.__nodeLog['n1'] || [];
process.stdout.write(String(log.length));
`
	out, err := exec.Command("node", "-e", harness, uiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	if got != "2" {
		t.Errorf("node-log chunk count = %s, want 2 (reconnect duplicated chunks)", got)
	}
}
