package server

import (
	"strings"
	"testing"
)

// TestBuildVarFields drives the real run-form field builder from the embedded
// UI (run-workflow-spec §"The Run modal"): one field per declared var,
// prefilled from the item, with `repo` special-cased as the repo dropdown
// rather than a text input.
func TestBuildVarFields(t *testing.T) {
	html := evalUI(t, `buildVarFields(['identifier','url'], {url:'http://x/1'}, [])`)
	for _, want := range []string{`data-var="identifier"`, `data-var="url"`, `value="http://x/1"`} {
		if !strings.Contains(html, want) {
			t.Errorf("var fields missing %q:\n%s", want, html)
		}
	}
	// A var with no prefill renders an empty field, not the string "undefined".
	if strings.Contains(html, "undefined") {
		t.Errorf("unprefilled var leaked undefined:\n%s", html)
	}

	// `repo` renders as a <select> of registered repos, not an <input>.
	repoHTML := evalUI(t, `buildVarFields(['repo'], {repo:'a/b'}, [{name:'a/b',path:'/p'},{name:'c/d',path:'/q'}])`)
	if !strings.Contains(repoHTML, `<select class="run-var" data-var="repo">`) {
		t.Errorf("repo var did not render as a select:\n%s", repoHTML)
	}
	if !strings.Contains(repoHTML, `<option value="a/b" selected>`) {
		t.Errorf("prefilled repo not pre-selected:\n%s", repoHTML)
	}
	if !strings.Contains(repoHTML, `value="c/d"`) {
		t.Errorf("repo select missing a registered repo:\n%s", repoHTML)
	}
	if strings.Contains(repoHTML, `data-var="repo" value=`) {
		t.Errorf("repo var rendered as an input:\n%s", repoHTML)
	}
}

// TestBuildVarFieldsEscapes: a var name and a prefilled value are inert data,
// escaped before they reach markup (mirrors the page's XSS discipline).
func TestBuildVarFieldsEscapes(t *testing.T) {
	html := evalUI(t, `buildVarFields(['x'], {x:'<img src=x onerror=alert(1)>'}, [])`)
	if strings.Contains(html, "<img src=x onerror=") {
		t.Errorf("prefill rendered as live markup:\n%s", html)
	}
}

// TestVarsFromFields: the form's field nodes reshape into the {var:value}
// map the run POST carries. Empty fields are omitted so an item-prefilled var
// the human left blank is NOT overlaid as "" (which would blank the server's
// item base); values are trimmed.
func TestVarsFromFields(t *testing.T) {
	out := evalUI(t, `JSON.stringify(varsFromFields([`+
		`{dataset:{var:'identifier'},value:' ENG-42 '},`+
		`{dataset:{var:'title'},value:''},`+
		`{dataset:{var:'repo'},value:'a/b'}]))`)
	if out != `{"identifier":"ENG-42","repo":"a/b"}` {
		t.Errorf("varsFromFields = %s, want identifier+repo only, trimmed", out)
	}
}
