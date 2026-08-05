package server

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runLoadArtifacts drives the real loadArtifacts from the embedded index.html
// against a fake fetch where GET /pipelines/run1/diff returns diffBody. It
// reports the Diff region's HTML and every URL fetched, so a test can assert
// which diff source won (ui-tailwind-spec T9c).
func runLoadArtifacts(t *testing.T, diffBody string) (diffHTML string, fetched []string) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping UI wiring test")
	}
	uiPath, err := filepath.Abs("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(diffBody)

	harness := `
const fs = require('fs');
const vm = require('vm');
const html = fs.readFileSync(process.argv[1], 'utf8');
const m = html.match(/<script>([\s\S]*?)<\/script>/);
if (!m) { console.error('no <script> found'); process.exit(2); }

const els = {};
const el = () => ({ innerHTML: '', value: '',
  querySelectorAll: () => [], querySelector: () => null, firstElementChild: null,
  insertAdjacentHTML(pos, h) { this.innerHTML += h; },
  classList: { toggle() {}, add() {}, remove() {} }, setAttribute() {} });
const document = { getElementById: (id) => (els[id] = els[id] || el()) };

const list = { entries: [
  { path: 'artifacts/changes.diff', size: 40, is_dir: false },
  { path: 'events.jsonl', size: 12, is_dir: false },
] };
const jjDiff = ` + string(body) + `;
const fetched = [];
const fetch = (url) => {
  fetched.push(url);
  if (url === '/pipelines/run1/artifacts')
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(list) });
  if (url === '/pipelines/run1/diff')
    return Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve(jjDiff) });
  if (url === '/pipelines/run1/artifacts/artifacts/changes.diff')
    return Promise.resolve({ ok: true, status: 200, text: () => Promise.resolve('@@ -1 +1 @@\n-old\n+artifact-fallback\n') });
  return Promise.resolve({ ok: false, status: 404, text: () => Promise.resolve('') });
};

const sandbox = { window: { addEventListener() {} }, document, location: {},
  console, fetch };
vm.createContext(sandbox);
vm.runInContext(m[1] + '\nglobalThis.__setRun = (r) => { currentRun = r; };', sandbox);

sandbox.__setRun('run1');
sandbox.loadArtifacts().then(() => {
  process.stdout.write(JSON.stringify({
    diffHtml: els['run-diff-body'].innerHTML,
    fetched,
  }));
}).catch(e => { console.error(e); process.exit(3); });
`
	out, err := exec.Command("node", "-e", harness, uiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	var got struct {
		DiffHTML string   `json:"diffHtml"`
		Fetched  []string `json:"fetched"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode harness output: %v\n%s", err, out)
	}
	return got.DiffHTML, got.Fetched
}

// TestLoadArtifactsPrefersJJDiff: when GET /diff returns a diff, the Diff panel
// renders it and the *.diff artifact is never fetched — the jj-derived range
// is the source of truth (T9c).
func TestLoadArtifactsPrefersJJDiff(t *testing.T) {
	diffHTML, fetched := runLoadArtifacts(t, "diff --git a/foo b/foo\n@@ -1 +1 @@\n-x\n+jj-derived\n")

	for _, want := range []string{"jj-derived", "diff-add", "diff-del"} {
		if !strings.Contains(diffHTML, want) {
			t.Errorf("diff panel missing %q:\n%s", want, diffHTML)
		}
	}
	if strings.Contains(diffHTML, "artifact-fallback") {
		t.Errorf("diff panel rendered the artifact instead of the jj diff:\n%s", diffHTML)
	}
	for _, u := range fetched {
		if strings.Contains(u, "changes.diff") {
			t.Errorf("artifact diff fetched despite a jj diff being available: %v", fetched)
		}
	}
}

// TestLoadArtifactsFallsBackToArtifact: an empty GET /diff (VM / non-jj run)
// falls back to auto-rendering the first *.diff artifact (T9c).
func TestLoadArtifactsFallsBackToArtifact(t *testing.T) {
	diffHTML, _ := runLoadArtifacts(t, "")

	if !strings.Contains(diffHTML, "artifact-fallback") {
		t.Errorf("empty jj diff should fall back to the artifact:\n%s", diffHTML)
	}
}
