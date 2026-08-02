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

// TestConfigReposPanelEmpty: with no registered repos the panel shows an
// empty-state hint (not a bare header-only table) and still offers add-repo
// so the first repo can be entered (config-screen-spec C5 empty states).
func TestConfigReposPanelEmpty(t *testing.T) {
	html := evalUI(t, `reposPanelHtml({})`)
	if !strings.Contains(strings.ToLower(html), "no repos") {
		t.Errorf("empty repos panel should show a hint:\n%s", html)
	}
	if !strings.Contains(html, "data-repo-add") {
		t.Errorf("empty repos panel should still offer add-repo:\n%s", html)
	}
}

// TestConfigProvidersPanelEmpty: with no providers the panel shows an
// empty-state hint and still offers add-provider (C5 empty states).
func TestConfigProvidersPanelEmpty(t *testing.T) {
	html := evalUI(t, `providersPanelHtml({default_provider:"",providers:{}})`)
	if !strings.Contains(strings.ToLower(html), "no providers") {
		t.Errorf("empty providers panel should show a hint:\n%s", html)
	}
	if !strings.Contains(html, "data-provider-add") {
		t.Errorf("empty providers panel should still offer add-provider:\n%s", html)
	}
}

// TestRepoWarning matches a soft save-warning back to its repo by the
// backend's `repo %q:` format, so an unresolved-path warning lands inline on
// the offending row and not on a same-prefixed sibling (config-screen-spec
// C5 validation surfaces — reject vs warn).
func TestRepoWarning(t *testing.T) {
	ws := `['repo "a/b": path "/x" does not resolve to a directory']`
	if got := evalUI(t, `repoWarning("a/b", `+ws+`)`); got == "" {
		t.Errorf("repoWarning should match the a/b warning, got empty")
	}
	if got := evalUI(t, `repoWarning("a/bc", `+ws+`)`); got != "" {
		t.Errorf("repoWarning should not match a same-prefixed sibling, got %q", got)
	}
	if got := evalUI(t, `repoWarning("c/d", `+ws+`)`); got != "" {
		t.Errorf("repoWarning should not match an unrelated repo, got %q", got)
	}
}

// TestConfigReposPanelWarns: a soft warning for a repo surfaces inline on
// that row (not just the top banner), so the user sees which path failed.
func TestConfigReposPanelWarns(t *testing.T) {
	html := evalUI(t, `reposPanelHtml({"a/b":{path:"/x",checks:{}},"c/d":{path:"/home",checks:{}}},`+
		`['repo "a/b": path "/x" does not resolve to a directory'])`)
	if !strings.Contains(html, "data-repo-warning") {
		t.Errorf("warned repo should carry an inline marker:\n%s", html)
	}
	// The marker text names the failure and is escaped (external string).
	if !strings.Contains(html, "does not resolve") {
		t.Errorf("inline warning should carry the failure text:\n%s", html)
	}
}

// TestConfigReposPanelNoWarn: absent warnings render no inline marker.
func TestConfigReposPanelNoWarn(t *testing.T) {
	html := evalUI(t, `reposPanelHtml({"a/b":{path:"/home",checks:{}}})`)
	if strings.Contains(html, "data-repo-warning") {
		t.Errorf("unwarned panel should carry no inline marker:\n%s", html)
	}
}

// TestConfigReposPanelRunnerImage: a repo's declared runner + vm.image
// render as selects — runner offers the fixed direct|local|vm placements
// (the declared one selected), image offers the registered vm_images names
// (the declared one selected). The row carries the save-collector hooks
// (per-repo VM config, VM4).
func TestConfigReposPanelRunnerImage(t *testing.T) {
	html := evalUI(t, `reposPanelHtml({"a/b":{path:"/h",checks:{},runner:"vm",vm:{image:"node-ts"}}},[],`+
		`{default:".#vm-runner","node-ts":".#vm-runner"})`)

	for _, want := range []string{"data-repo-runner", "data-repo-image",
		`<option value="direct"`, `<option value="local"`, `<option value="vm"`} {
		if !strings.Contains(html, want) {
			t.Errorf("repos panel missing %q:\n%s", want, html)
		}
	}
	// The declared runner + image are the selected options.
	if !strings.Contains(html, `value="vm" selected`) {
		t.Errorf("declared runner should be selected:\n%s", html)
	}
	if !strings.Contains(html, `value="node-ts" selected`) {
		t.Errorf("declared image should be selected:\n%s", html)
	}
}

// TestConfigReposPanelImageOptionsFromRegistry: the image select is populated
// from the registered vm_images names — a name absent from the registry is
// not offered (VM4: the UI references registered images, never invents them).
func TestConfigReposPanelImageOptionsFromRegistry(t *testing.T) {
	html := evalUI(t, `reposPanelHtml({"a/b":{path:"/h",checks:{},runner:"vm"}},[],`+
		`{default:".#vm-runner",python:"/nix/store/x"})`)
	for _, want := range []string{`value="default"`, `value="python"`} {
		if !strings.Contains(html, want) {
			t.Errorf("image select missing registered name %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, `value="node-ts"`) {
		t.Errorf("image select should offer only registered names:\n%s", html)
	}
}

// TestConfigReposPanelImageEscapes: a registry image name is external and is
// escaped in both the option value and its label.
func TestConfigReposPanelImageEscapes(t *testing.T) {
	html := evalUI(t, `reposPanelHtml({"a/b":{path:"/h",checks:{},runner:"vm"}},[],`+
		`{"<img src=x onerror=alert(1)>":".#x"})`)
	if strings.Contains(html, "<img src=x onerror=") {
		t.Errorf("registry image name rendered as live markup:\n%s", html)
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

// TestConfigLinearPanelArmedClear: an armed pending-clear disables the input,
// announces the clear, and flips the control to an un-arm ("keep key") — so
// the clear is reversible via re-render, not a one-way DOM mutation
// (config-screen-spec C5 redaction UX).
func TestConfigLinearPanelArmedClear(t *testing.T) {
	html := evalUI(t, `linearPanelHtml({api_key_set:true}, true)`)
	if !strings.Contains(html, "will be cleared") {
		t.Errorf("armed clear should announce the pending clear:\n%s", html)
	}
	if !strings.Contains(html, "disabled") {
		t.Errorf("armed clear should disable the key input:\n%s", html)
	}
	if !strings.Contains(html, "keep key") {
		t.Errorf("armed clear should offer an un-arm control:\n%s", html)
	}
}

// TestConfigLinearPanelNotArmed: without an armed clear the input is live,
// there is no clear announcement, and the control reads "clear key".
func TestConfigLinearPanelNotArmed(t *testing.T) {
	html := evalUI(t, `linearPanelHtml({api_key_set:true}, false)`)
	if strings.Contains(html, "will be cleared") {
		t.Errorf("unarmed panel should not announce a clear:\n%s", html)
	}
	if strings.Contains(html, "disabled") {
		t.Errorf("unarmed panel should leave the key input live:\n%s", html)
	}
	if !strings.Contains(html, "clear key") {
		t.Errorf("unarmed panel should offer the clear control:\n%s", html)
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

// TestConfigBuildPutBodyRunnerImage: a repo's declared runner + image ride
// into the PUT body (runner as a field, image nested under vm), and the
// vm_images registry round-trips unchanged — the UI references it but never
// edits it, so a save must not drop it (per-repo VM config, VM4).
func TestConfigBuildPutBodyRunnerImage(t *testing.T) {
	body := evalUI(t, `JSON.stringify(buildPutBody({`+
		`defaultProvider:"",providers:[],linearKey:"",linearClear:false,`+
		`vmImages:{default:".#vm-runner","node-ts":".#vm-runner"},`+
		`repos:[{name:"a/b",path:"/p",checks:{},runner:"vm",image:"node-ts"}]}))`)

	for _, want := range []string{`"runner":"vm"`, `"vm":{"image":"node-ts"}`, `"vm_images":{`, `".#vm-runner"`} {
		if !strings.Contains(body, want) {
			t.Errorf("PUT body missing %q:\n%s", want, body)
		}
	}
}

// TestConfigBuildPutBodyOmitsEmptyRunner: a repo that declares no runner
// sends neither a runner nor a vm key, so an untouched config file stays
// byte-unchanged (mirrors the schema's omitempty, VM2).
func TestConfigBuildPutBodyOmitsEmptyRunner(t *testing.T) {
	body := evalUI(t, `JSON.stringify(buildPutBody({`+
		`defaultProvider:"",providers:[],linearKey:"",linearClear:false,`+
		`repos:[{name:"a/b",path:"/p",checks:{},runner:"",image:""}]}))`)
	for _, unwanted := range []string{`"runner"`, `"vm"`, `"vm_images"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("PUT body should not contain %q for an unset runner:\n%s", unwanted, body)
		}
	}
}

// TestConfigBuildPutBodyImageOnlyWhenVM: a non-vm runner drops the image —
// vm.image is only meaningful under a vm runner (dispatch resolution §2), so
// a stale image left from an earlier vm selection is not persisted.
func TestConfigBuildPutBodyImageOnlyWhenVM(t *testing.T) {
	body := evalUI(t, `JSON.stringify(buildPutBody({`+
		`defaultProvider:"",providers:[],linearKey:"",linearClear:false,`+
		`repos:[{name:"a/b",path:"/p",checks:{},runner:"local",image:"node-ts"}]}))`)
	if !strings.Contains(body, `"runner":"local"`) {
		t.Errorf("PUT body should carry the local runner:\n%s", body)
	}
	if strings.Contains(body, `"vm"`) || strings.Contains(body, `node-ts`) {
		t.Errorf("PUT body should drop the image off a non-vm runner:\n%s", body)
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
