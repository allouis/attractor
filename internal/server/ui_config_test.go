package server

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// evalUI runs the embedded index.html <script> in a node vm sandbox, then
// evaluates expr (which may call any top-level function the script defines)
// and returns its stringified result. It is the UI's unit-test seam: pure
// render/reshape helpers are exercised as real JS, not string-matched
// (mirrors ui_xss_test.go).
func evalUI(t *testing.T, expr string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping UI JS test")
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
const sandbox = { window: { addEventListener() {} }, document: {}, location: {}, console };
vm.createContext(sandbox);
vm.runInContext(m[1], sandbox);
const out = vm.runInContext(process.argv[2], sandbox);
process.stdout.write(String(out));
`
	out, err := exec.Command("node", "-e", harness, uiPath, expr).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	return string(out)
}

// TestConfigReposPanel drives the real reposPanelHtml from the embedded UI:
// one editable row per registered repo with its path and the four standard
// check commands (config-screen-spec C4), every value escaped.
func TestConfigReposPanel(t *testing.T) {
	html := evalUI(t, `reposPanelHtml({"a/b":{path:"/home/a",checks:{deps:"pnpm i",test:"pnpm test"}}})`)

	for _, want := range []string{"a/b", "/home/a", "pnpm i", "pnpm test"} {
		if !strings.Contains(html, want) {
			t.Errorf("repos panel missing %q:\n%s", want, html)
		}
	}
	// All four standard checks surface as inputs, even the unset ones.
	for _, check := range []string{"deps", "typecheck", "lint", "test"} {
		if !strings.Contains(html, `data-check="`+check+`"`) {
			t.Errorf("repos panel missing check input %q:\n%s", check, html)
		}
	}
}

// TestConfigLinearPanelSet: the Linear panel reports a stored key as set,
// never echoes a value (it has none — GET redacts), and offers Clear.
func TestConfigLinearPanelSet(t *testing.T) {
	html := evalUI(t, `linearPanelHtml({api_key_set:true})`)
	if !strings.Contains(html, "set") {
		t.Errorf("linear panel should report the key as set:\n%s", html)
	}
	if !strings.Contains(html, `type="password"`) {
		t.Errorf("linear panel should use a secret input:\n%s", html)
	}
	if !strings.Contains(html, "data-linear-clear") {
		t.Errorf("a set key should offer a Clear control:\n%s", html)
	}
}

// TestConfigLinearPanelUnset: no stored key → reported unset, no Clear (there
// is nothing to remove).
func TestConfigLinearPanelUnset(t *testing.T) {
	html := evalUI(t, `linearPanelHtml({api_key_set:false})`)
	if !strings.Contains(html, "unset") {
		t.Errorf("linear panel should report the key as unset:\n%s", html)
	}
	if strings.Contains(html, "data-linear-clear") {
		t.Errorf("an unset key should not offer Clear:\n%s", html)
	}
}

// TestConfigReposPanelEscapes guards against a malicious repo name/path
// being rendered as live markup (the doc is daemon-owned but the panel must
// not become an injection sink).
func TestConfigReposPanelEscapes(t *testing.T) {
	html := evalUI(t, `reposPanelHtml({"<img src=x onerror=alert(1)>":{path:"</td><script>alert(2)</script>",checks:{}}})`)

	if strings.Contains(html, "<img src=x onerror=") {
		t.Errorf("repo name rendered as live markup:\n%s", html)
	}
	if strings.Contains(html, "<script>alert(2)</script>") {
		t.Errorf("repo path rendered as live markup:\n%s", html)
	}
}
