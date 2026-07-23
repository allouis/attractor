package server

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunListEscapesAttackerFields drives the UI's run-list row builder with
// a malicious graph_name and cwd and asserts the produced markup is inert
// (escaped), guarding against stored XSS in the run list. It evaluates the
// real runRowHtml from the embedded index.html via node's vm sandbox.
func TestRunListEscapesAttackerFields(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping UI XSS test")
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
if (typeof sandbox.runRowHtml !== 'function') { console.error('runRowHtml missing'); process.exit(2); }
const row = sandbox.runRowHtml({
  id: 'deadbeefdeadbeef',
  graph_name: '<img src=x onerror=alert(1)>',
  cwd: '</td><script>alert(2)</script>',
  status: 'running',
  started_at: '2026-07-25',
});
process.stdout.write(row);
`
	out, err := exec.Command("node", "-e", harness, uiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	row := string(out)

	if strings.Contains(row, "<img src=x onerror=") {
		t.Errorf("graph_name rendered as live markup (stored XSS):\n%s", row)
	}
	if strings.Contains(row, "<script>alert(2)</script>") {
		t.Errorf("cwd rendered as live markup (stored XSS):\n%s", row)
	}
	if !strings.Contains(row, "&lt;img src=x onerror=") {
		t.Errorf("graph_name was not escaped:\n%s", row)
	}
}
