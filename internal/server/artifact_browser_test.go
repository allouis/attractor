package server

import (
	"strings"
	"testing"
)

// TestArtifactListHtml drives the U4 artifact browser list (web-ui-v2-spec U4):
// each file becomes a clickable row carrying its logs-root-relative path and a
// human-readable size; directories are structural, not clickable rows; every
// path is escaped; an empty listing renders an empty state, not a bare box.
func TestArtifactListHtml(t *testing.T) {
	entries := `[{path:'artifacts/changes.diff',size:2048,is_dir:false},` +
		`{path:'artifacts',size:0,is_dir:true},` +
		`{path:'events.jsonl',size:120,is_dir:false}]`
	html := evalUI(t, "artifactListHtml("+entries+")")

	for _, want := range []string{"artifacts/changes.diff", "events.jsonl", `data-path="artifacts/changes.diff"`} {
		if !strings.Contains(html, want) {
			t.Errorf("artifact list missing %q:\n%s", want, html)
		}
	}
	// The directory is not a clickable file row.
	if strings.Contains(html, `data-path="artifacts"`) {
		t.Errorf("directory should not be a clickable file row:\n%s", html)
	}

	// Empty listing → an empty state.
	if empty := evalUI(t, "artifactListHtml([])"); !strings.Contains(empty, "empty") && strings.TrimSpace(empty) == "" {
		t.Errorf("empty listing should render an empty state, got:\n%s", empty)
	}

	// A path is attacker-controlled (a run can write arbitrary names).
	esc := evalUI(t, `artifactListHtml([{path:'<img src=x onerror=alert(1)>',size:1,is_dir:false}])`)
	if strings.Contains(esc, "<img src=x onerror=") {
		t.Errorf("artifact list rendered attacker markup live:\n%s", esc)
	}
}

// TestIsDiffPath drives the U4 diff-detection predicate: a .diff/.patch suffix
// or a bare "diff" name is a diff; ordinary stage files are not.
func TestIsDiffPath(t *testing.T) {
	for _, p := range []string{"changes.diff", "artifacts/x.patch", "diff", "sub/diff"} {
		if got := evalUI(t, "String(isDiffPath("+jsStr(p)+"))"); got != "true" {
			t.Errorf("isDiffPath(%q) = %s, want true", p, got)
		}
	}
	for _, p := range []string{"response.md", "events.jsonl", "diffident.txt"} {
		if got := evalUI(t, "String(isDiffPath("+jsStr(p)+"))"); got != "false" {
			t.Errorf("isDiffPath(%q) = %s, want false", p, got)
		}
	}
}
