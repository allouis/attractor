package server

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadArtifactsWiresBrowserAndDiff drives the real loadArtifacts +
// viewArtifact from the embedded index.html (web-ui-v2-spec U4) against a fake
// fetch: the artifact list (GET …/artifacts) fills the browser, the first
// diff-looking entry is auto-fetched and rendered coloured into the Diff
// region, and viewArtifact fetches an ordinary file into the inline viewer.
// Mirrors the node-vm harness in stage_select_test.go.
func TestLoadArtifactsWiresBrowserAndDiff(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping UI wiring test")
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

const list = { entries: [
  { path: 'artifacts', size: 0, is_dir: true },
  { path: 'artifacts/changes.diff', size: 40, is_dir: false },
  { path: 'events.jsonl', size: 12, is_dir: false },
] };
const bodies = {
  '/pipelines/run1/artifacts/artifacts/changes.diff': '@@ -1 +1 @@\n-old\n+new\n',
  '/pipelines/run1/artifacts/events.jsonl': 'hello events',
};
const fetch = (url) => {
  if (url === '/pipelines/run1/artifacts')
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(list) });
  if (bodies[url] !== undefined)
    return Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve(bodies[url]) });
  return Promise.resolve({ ok: false, status: 404, text: () => Promise.resolve('') });
};

const sandbox = { window: { addEventListener() {} }, document, location: {},
  console, fetch };
vm.createContext(sandbox);
vm.runInContext(m[1] + '\nglobalThis.__setRun = (r) => { currentRun = r; };', sandbox);

sandbox.__setRun('run1');
sandbox.loadArtifacts().then(() => sandbox.viewArtifact('events.jsonl')).then(() => {
  process.stdout.write(JSON.stringify({
    listHtml: els['run-artifacts-list'].innerHTML,
    diffHtml: els['run-diff-body'].innerHTML,
    viewHtml: els['run-artifact-view'].innerHTML,
  }));
}).catch(e => { console.error(e); process.exit(3); });
`
	out, err := exec.Command("node", "-e", harness, uiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	got := string(out)

	// The browser lists the files.
	for _, want := range []string{"changes.diff", "events.jsonl", `data-path=`} {
		if !strings.Contains(got, want) {
			t.Errorf("artifact browser missing %q:\n%s", want, got)
		}
	}
	// The first diff-looking artifact auto-renders coloured.
	for _, want := range []string{"diff-add", "diff-del"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff region missing %q:\n%s", want, got)
		}
	}
	// viewArtifact shows an ordinary file inline.
	if !strings.Contains(got, "hello events") {
		t.Errorf("inline viewer missing file body:\n%s", got)
	}
}
