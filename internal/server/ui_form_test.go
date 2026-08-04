package server

import (
	"strings"
	"testing"
)

// T6c adopts the Tailwind Plus form-layout (docs/tailwind-components/form-layout.html)
// for the run-form modal and the Config form fields, token-remapped: a strong
// ink label over a full-width, ≥40px-tappable control with an accent focus
// ring — no stock gray/indigo. These drive the real render fns via evalUI.

// formControlWant is the token-remapped control shape every converted field
// carries; formLabelWant is the label shape.
var (
	formControlWant = []string{"w-full", "min-h-10", "rounded-md", "border-line", "bg-surface-1", "focus:outline-accent"}
	formLabelWant   = []string{"font-medium", "text-ink"}
)

func assertNoStockPalette(t *testing.T, name, html string) {
	t.Helper()
	for _, bad := range []string{"gray-", "indigo-"} {
		if strings.Contains(html, bad) {
			t.Errorf("%s leaked a hardcoded palette %q:\n%s", name, bad, html)
		}
	}
}

// TestRunFormFieldsFormLayout: the run-form var fields adopt the form-layout —
// a token label + full-width tappable control — while keeping the .run-var /
// data-var hooks the submit path reads and the repo select.
func TestRunFormFieldsFormLayout(t *testing.T) {
	html := evalUI(t, `buildVarFields(['url'], {url:'http://x/1'}, [])`)
	for _, want := range append(append([]string{}, formControlWant...), formLabelWant...) {
		if !strings.Contains(html, want) {
			t.Errorf("run-form var field missing form-layout class %q:\n%s", want, html)
		}
	}
	// Behaviour preserved: the submit path still finds the field by class + data-var.
	if !strings.Contains(html, "run-var") || !strings.Contains(html, `data-var="url"`) {
		t.Errorf("run-form var field dropped its .run-var/data-var hook:\n%s", html)
	}
	if !strings.Contains(html, `value="http://x/1"`) {
		t.Errorf("run-form var field dropped its prefill:\n%s", html)
	}
	assertNoStockPalette(t, "run-form var field", html)

	// `repo` stays a <select> of registered repos, now form-layout styled.
	repo := evalUI(t, `buildVarFields(['repo'], {repo:'a/b'}, [{name:'a/b',path:'/p'}])`)
	if !strings.Contains(repo, "<select ") || !strings.Contains(repo, `data-var="repo"`) {
		t.Errorf("repo var no longer a select:\n%s", repo)
	}
	for _, want := range formControlWant {
		if !strings.Contains(repo, want) {
			t.Errorf("repo select missing form-layout class %q:\n%s", want, repo)
		}
	}
	assertNoStockPalette(t, "repo select", repo)
}

// TestConfigFieldsFormLayout: the Linear key input and the default-provider
// select adopt the same token-remapped form-layout control.
func TestConfigFieldsFormLayout(t *testing.T) {
	linear := evalUI(t, `linearPanelHtml({api_key_set:false})`)
	if !strings.Contains(linear, `type="password"`) {
		t.Errorf("linear key must stay a secret input:\n%s", linear)
	}
	for _, want := range formControlWant {
		if !strings.Contains(linear, want) {
			t.Errorf("linear key input missing form-layout class %q:\n%s", want, linear)
		}
	}
	assertNoStockPalette(t, "linear panel", linear)

	prov := evalUI(t, `providersPanelHtml({default_provider:'',providers:{}})`)
	// The default-provider select (id config-default-provider) is a form field.
	for _, want := range formControlWant {
		if !strings.Contains(prov, want) {
			t.Errorf("default-provider select missing form-layout class %q:\n%s", want, prov)
		}
	}
	assertNoStockPalette(t, "providers panel", prov)
}
