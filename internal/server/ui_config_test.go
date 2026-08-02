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

// TestConfigProvidersPanel: the Providers panel renders the default-provider
// choice, each provider's backend/command/model_env, and the restart note
// (providers are startup-built — config-screen-spec effect timing).
func TestConfigProvidersPanel(t *testing.T) {
	html := evalUI(t, `providersPanelHtml({default_provider:"anthropic",providers:{anthropic:{backend:"acp",command:"claude-agent-acp",model_env:"ANTHROPIC_MODEL"}}})`)

	for _, want := range []string{"anthropic", "acp", "claude-agent-acp", "ANTHROPIC_MODEL"} {
		if !strings.Contains(html, want) {
			t.Errorf("providers panel missing %q:\n%s", want, html)
		}
	}
	if !strings.Contains(strings.ToLower(html), "restart") {
		t.Errorf("providers panel missing the restart-to-apply note:\n%s", html)
	}
	if !strings.Contains(html, `id="config-default-provider"`) {
		t.Errorf("providers panel missing the default-provider control:\n%s", html)
	}
}

// TestConfigProvidersPanelEscapes: a malicious provider name is inert.
func TestConfigProvidersPanelEscapes(t *testing.T) {
	html := evalUI(t, `providersPanelHtml({default_provider:"",providers:{"<img src=x onerror=alert(1)>":{backend:"acp"}}})`)
	if strings.Contains(html, "<img src=x onerror=") {
		t.Errorf("provider name rendered as live markup:\n%s", html)
	}
}

// TestConfigBuildPutBody: the collected form reshapes into the PUT /config
// Document — provider/repo rows to maps (blank-name rows dropped), and the
// Linear secret handled by secret-merge rules: an empty key omits the field
// (preserve), a value replaces, an armed clear sets api_key_clear.
func TestConfigBuildPutBody(t *testing.T) {
	body := evalUI(t, `JSON.stringify(buildPutBody({`+
		`defaultProvider:"anthropic",`+
		`providers:[{name:"anthropic",backend:"acp",command:"claude-agent-acp",model_env:"ANTHROPIC_MODEL"},{name:"",backend:"drop"}],`+
		`repos:[{name:"a/b",path:"/p",checks:{deps:"pnpm i"}},{name:"",path:"/drop",checks:{}}],`+
		`linearKey:"",linearClear:false}))`)

	for _, want := range []string{`"default_provider":"anthropic"`, `"anthropic":{`, `"backend":"acp"`, `"a/b":{`, `"/p"`, `"deps":"pnpm i"`, `"api_key":""`} {
		if !strings.Contains(body, want) {
			t.Errorf("PUT body missing %q:\n%s", want, body)
		}
	}
	// Blank-name rows are dropped, not sent as "" keys.
	for _, unwanted := range []string{`"drop"`, `"/drop"`, `api_key_clear`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("PUT body should not contain %q:\n%s", unwanted, body)
		}
	}
}

// TestConfigBuildPutBodySecrets: a typed key replaces; an armed clear sets
// the clear signal.
func TestConfigBuildPutBodySecrets(t *testing.T) {
	set := evalUI(t, `JSON.stringify(buildPutBody({defaultProvider:"",providers:[],repos:[],linearKey:"lin_new",linearClear:false}))`)
	if !strings.Contains(set, `"api_key":"lin_new"`) {
		t.Errorf("typed key should be sent:\n%s", set)
	}
	cleared := evalUI(t, `JSON.stringify(buildPutBody({defaultProvider:"",providers:[],repos:[],linearKey:"",linearClear:true}))`)
	if !strings.Contains(cleared, `"api_key_clear":true`) {
		t.Errorf("armed clear should set api_key_clear:\n%s", cleared)
	}
}

// TestConfigSaveMessage: the save-result classifier maps the PUT response to
// a banner — structural 422 error (nothing saved), soft-warning 200 (saved),
// clean 200 (saved), or a generic failure.
func TestConfigSaveMessage(t *testing.T) {
	cases := []struct{ expr, kind, textPart string }{
		{`JSON.stringify(saveMessage(200,{warnings:[]}))`, "ok", "aved"},
		{`JSON.stringify(saveMessage(200,{warnings:["repo x: path does not resolve"]}))`, "warn", "path does not resolve"},
		{`JSON.stringify(saveMessage(422,{error:"unknown backend bogus"}))`, "error", "unknown backend bogus"},
		{`JSON.stringify(saveMessage(500,{}))`, "error", ""},
	}
	for _, c := range cases {
		got := evalUI(t, c.expr)
		if !strings.Contains(got, `"kind":"`+c.kind+`"`) {
			t.Errorf("%s → want kind %q, got %s", c.expr, c.kind, got)
		}
		if c.textPart != "" && !strings.Contains(got, c.textPart) {
			t.Errorf("%s → want text containing %q, got %s", c.expr, c.textPart, got)
		}
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
