package server

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadArtifactsGuardsRunSwitch drives the U4 stale-render guard: if the
// user opens another run while a slow artifact list is still in flight, the
// first run's data must not be painted into the shared run-detail DOM. The
// list fetch for run1 is held pending until after the view has switched to
// run2; loadArtifacts must then bail rather than overwrite run2's panel.
func TestLoadArtifactsGuardsRunSwitch(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping UI race test")
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

const els = {};
const el = () => ({ innerHTML: '', value: '', scrollTop: 0, scrollHeight: 0,
  querySelectorAll: () => [], querySelector: () => null, firstElementChild: null,
  insertAdjacentHTML(pos, h) { this.innerHTML += h; },
  classList: { toggle() {}, add() {}, remove() {} }, setAttribute() {} });
const document = { getElementById: (id) => (els[id] = els[id] || el()) };

// The run1 list fetch is held pending until we release it after switching runs.
let releaseList;
const listReady = new Promise(res => { releaseList = res; });
const run1List = { entries: [{ path: 'run1-secret.txt', size: 9, is_dir: false }] };
const fetch = (url) => {
  if (url === '/pipelines/run1/artifacts')
    return listReady.then(() => ({ ok: true, status: 200, json: () => Promise.resolve(run1List) }));
  return Promise.resolve({ ok: false, status: 404, text: () => Promise.resolve('') });
};

const sandbox = { window: { addEventListener() {} }, document, location: {}, console, fetch };
vm.createContext(sandbox);
vm.runInContext(m[1] + '\nglobalThis.__setRun = (r) => { currentRun = r; };', sandbox);

sandbox.__setRun('run1');
const done = sandbox.loadArtifacts();   // starts, blocks on the held list fetch
sandbox.__setRun('run2');               // user opens a different run
releaseList();                          // now the stale run1 list resolves
done.then(() => {
  process.stdout.write(els['run-artifacts-list'].innerHTML);
}).catch(e => { console.error(e); process.exit(3); });
`
	out, err := exec.Command("node", "-e", harness, uiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	got := string(out)
	if strings.Contains(got, "run1-secret.txt") || strings.Contains(got, "artifact-row") {
		t.Errorf("stale run1 artifacts painted after switching to run2:\n%s", got)
	}
}
