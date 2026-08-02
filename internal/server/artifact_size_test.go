package server

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runArtifactHarness runs the embedded UI script in a node vm with the given
// fetch/body wiring and driver, returning the driver's stdout. Shared by the
// U4 size-cap tests.
func runArtifactHarness(t *testing.T, driver string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping UI size-cap test")
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
globalThis.__els = els;
globalThis.__fetched = [];
` + driver + `
`
	out, err := exec.Command("node", "-e", harness, uiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	return string(out)
}

// TestLoadArtifactsSkipsHugeDiff proves the U4 diff auto-render refuses a diff
// whose declared size exceeds the render cap: it neither fetches nor renders
// the body (which would freeze the tab), showing a "too large" note instead.
func TestLoadArtifactsSkipsHugeDiff(t *testing.T) {
	got := runArtifactHarness(t, `
const fetch = (url) => {
  __fetched.push(url);
  if (url === '/pipelines/run1/artifacts')
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(
      { entries: [{ path: 'huge.diff', size: 99999999, is_dir: false }] }) });
  return Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve('@@\n+x\n') });
};
const sandbox = { window: { addEventListener() {} }, document, location: {}, console, fetch,
  __els, __fetched };
vm.createContext(sandbox);
vm.runInContext(m[1] + '\nglobalThis.__setRun = (r) => { currentRun = r; };', sandbox);
sandbox.__setRun('run1');
sandbox.loadArtifacts().then(() => {
  process.stdout.write(JSON.stringify({
    diff: __els['run-diff-body'].innerHTML,
    fetched: __fetched,
  }));
}).catch(e => { console.error(e); process.exit(3); });
`)
	if !strings.Contains(got, "too large") {
		t.Errorf("huge diff should show a too-large note:\n%s", got)
	}
	if strings.Contains(got, "diff-add") {
		t.Errorf("huge diff body should not be rendered:\n%s", got)
	}
	if strings.Contains(got, "huge.diff\"") && strings.Contains(got, "/artifacts/huge.diff") {
		t.Errorf("huge diff body should not be fetched:\n%s", got)
	}
}

// TestViewArtifactTruncatesHugeFile proves viewArtifact caps how much it
// renders: a body larger than the render cap is truncated with a note, so a
// giant file can't freeze the browser.
func TestViewArtifactTruncatesHugeFile(t *testing.T) {
	got := runArtifactHarness(t, `
const sandbox = { window: { addEventListener() {} }, document, location: {}, console,
  __els, __fetched };
vm.createContext(sandbox);
vm.runInContext(m[1] + '\nglobalThis.__setRun = (r) => { currentRun = r; };' +
  '\nglobalThis.__cap = ARTIFACT_RENDER_CAP;', sandbox);
const big = 'x'.repeat(sandbox.__cap + 500);
sandbox.fetch = (url) => Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve(big) });
sandbox.__setRun('run1');
sandbox.viewArtifact('big.txt').then(() => {
  const html = __els['run-artifact-view'].innerHTML;
  process.stdout.write(JSON.stringify({ len: html.length, cap: sandbox.__cap, html: html.slice(0, 200) + '…' + html.slice(-200) }));
}).catch(e => { console.error(e); process.exit(3); });
`)
	if !strings.Contains(got, "truncated") {
		t.Errorf("huge file view should note truncation:\n%s", got)
	}
}
