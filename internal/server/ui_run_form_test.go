package server

import (
	"os"
	"strings"
	"testing"
)

// funcSource slices the body of the named `function name(…) { … }` out of the
// page source, up to its closing top-level brace, so an assertion can scope to
// one function instead of matching a token anywhere on the page.
func funcSource(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "function "+name+"(")
	if start < 0 {
		t.Fatalf("function %s not found in page source", name)
	}
	end := strings.Index(src[start:], "\n}")
	if end < 0 {
		t.Fatalf("no closing brace found for function %s", name)
	}
	return src[start : start+end]
}

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

// TestMissingRequired: every declared var is a required input (the engine fails
// a fresh run whose vars= key is unseeded). The form blocks submit on an empty
// required field — but a var an item prefills server-side is NOT missing even
// when its field is blank, so item-driven runs aren't wrongly blocked
// (run-workflow-spec §"Required-var handling").
func TestMissingRequired(t *testing.T) {
	// Empty field, nothing prefills it → missing.
	out := evalUI(t, `JSON.stringify(missingRequired([`+
		`{dataset:{var:'repo'},value:''},`+
		`{dataset:{var:'identifier'},value:'ENG-42'}], {}))`)
	if out != `["repo"]` {
		t.Errorf("missingRequired = %s, want [\"repo\"]", out)
	}
	// Empty field but the item base supplies it → not missing.
	out = evalUI(t, `JSON.stringify(missingRequired([`+
		`{dataset:{var:'identifier'},value:''}], {identifier:'ENG-9'}))`)
	if out != `[]` {
		t.Errorf("item-prefilled var wrongly flagged missing: %s", out)
	}
	// Whitespace-only counts as empty; a filled field is present.
	out = evalUI(t, `JSON.stringify(missingRequired([`+
		`{dataset:{var:'title'},value:'  '},`+
		`{dataset:{var:'url'},value:'http://x'}], {}))`)
	if out != `["title"]` {
		t.Errorf("missingRequired = %s, want [\"title\"] (whitespace is empty)", out)
	}

	// submitRun gates on it before dispatching the POST (wiring guard).
	src, err := os.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(funcSource(t, string(src), "submitRun"), "missingRequired(") {
		t.Errorf("submitRun does not block on missingRequired before posting")
	}
}

// TestWorkflowRunButton: the Workflows view's per-row Run action carries the
// workflow name on a data-* attribute (the standalone launch entry point,
// run-workflow-spec §"Standalone launch"), escaped.
func TestWorkflowRunButton(t *testing.T) {
	html := evalUI(t, `workflowRunButton('review-pr')`)
	if !strings.Contains(html, `data-run-workflow="review-pr"`) {
		t.Errorf("run button missing the workflow handle:\n%s", html)
	}
	esc := evalUI(t, `workflowRunButton('<x>')`)
	if strings.Contains(esc, "<x>") {
		t.Errorf("run button did not escape the workflow name:\n%s", esc)
	}
}

// TestNeedsItemNote: a workflow with no fillable vars (only the synthesized
// repo) is item-driven — router reads item.type at runtime, which a standalone
// launch can't supply. The Workflows-view form warns (run-workflow-spec §"The
// input contract"). A workflow with real vars, or one opened from an item,
// gets no note.
func TestNeedsItemNote(t *testing.T) {
	note := evalUI(t, `needsItemNote([], false)`)
	if note == "" {
		t.Errorf("router standalone got no item note")
	}
	if !strings.Contains(note, "#items") {
		t.Errorf("item note does not link to the items view:\n%s", note)
	}
	if got := evalUI(t, `needsItemNote(['identifier'], false)`); got != "" {
		t.Errorf("workflow with a fillable var should get no note, got:\n%s", got)
	}
	if got := evalUI(t, `needsItemNote([], true)`); got != "" {
		t.Errorf("item-linked run should get no note, got:\n%s", got)
	}
	// `repo` is synthesized, not a fillable var, so a repo-only workflow is
	// still item-driven and gets the note.
	if got := evalUI(t, `needsItemNote(['repo'], false)`); got == "" {
		t.Errorf("repo-only workflow should get the item note")
	}
}

// TestRunFieldBody: the modal's field body always carries the repo picker (the
// cwd resolver, synthesized even when a workflow declares no vars), one field
// per declared var, and the item-driven note only for a var-less standalone
// launch (run-workflow-spec §"The input contract").
func TestRunFieldBody(t *testing.T) {
	// A workflow with a fillable var, standalone: repo picker + the var, no note.
	body := evalUI(t, `runFieldBody(['identifier'], {}, [{name:'a/b',path:'/p'}], false)`)
	if !strings.Contains(body, `data-var="repo"`) {
		t.Errorf("field body missing the synthesized repo picker:\n%s", body)
	}
	if !strings.Contains(body, `data-var="identifier"`) {
		t.Errorf("field body missing the declared var:\n%s", body)
	}
	if strings.Contains(body, "needs an") {
		t.Errorf("workflow with a fillable var should get no item note:\n%s", body)
	}

	// A var-less workflow, standalone: repo picker + the note.
	router := evalUI(t, `runFieldBody([], {}, [{name:'a/b',path:'/p'}], false)`)
	if !strings.Contains(router, `data-var="repo"`) {
		t.Errorf("var-less field body missing the repo picker:\n%s", router)
	}
	if !strings.Contains(router, "needs an") {
		t.Errorf("var-less standalone launch missing the item note:\n%s", router)
	}

	// A var-less workflow opened from an item: repo picker, no note.
	linked := evalUI(t, `runFieldBody([], {repo:'a/b'}, [{name:'a/b',path:'/p'}], true)`)
	if strings.Contains(linked, "needs an") {
		t.Errorf("item-linked run should get no item note:\n%s", linked)
	}
	// repo declared explicitly is not duplicated into a second picker.
	dup := evalUI(t, `runFieldBody(['repo'], {}, [{name:'a/b',path:'/p'}], false)`)
	if strings.Count(dup, `data-var="repo"`) != 1 {
		t.Errorf("repo picker duplicated when repo is a declared var:\n%s", dup)
	}
}

// TestOnWorkflowChangeLoading: selecting a workflow shows a loading placeholder
// in the field body while its vars are fetched (run-workflow-spec §R5,
// empty/loading states), so the body doesn't flash blank. Source-level like the
// migration guard — the fetch path is async DOM, verified separately.
func TestOnWorkflowChangeLoading(t *testing.T) {
	src, err := os.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	fn := funcSource(t, string(src), "onWorkflowChange")
	if !strings.Contains(fn, "loadingState(") {
		t.Errorf("onWorkflowChange does not show a loading state while fetching vars:\n%s", fn)
	}
}

// TestRunFormDialogOutsideRoutedSections: the router hides every `main >
// section` except the active route via display:none. The run-form <dialog>
// must live OUTSIDE those sections (a top-level sibling of <main>) — nested in
// a routed section it opens at 0x0 whenever any other route is active, so the
// Workflows-list and Run-detail re-run entry points render an invisible modal.
func TestRunFormDialogOutsideRoutedSections(t *testing.T) {
	src, err := os.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(src)
	dialog := strings.Index(html, `<dialog id="run-form"`)
	if dialog < 0 {
		t.Fatal("run-form dialog not found in page source")
	}
	mainEnd := strings.Index(html, "</main>")
	if mainEnd < 0 {
		t.Fatal("</main> not found in page source")
	}
	if dialog < mainEnd {
		t.Errorf("run-form dialog is inside <main> (offset %d < </main> at %d); "+
			"a routed section's display:none makes it open at 0x0", dialog, mainEnd)
	}
}

// TestRunFormMigratedOffItemsRun: the Run modal now funnels through the single
// admission endpoint POST /workflows/{name}/run (run-workflow-spec §Endpoints).
// The superseded /items/run must not be referenced anywhere in the UI.
func TestRunFormMigratedOffItemsRun(t *testing.T) {
	src, err := os.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(src)
	if strings.Contains(html, "/items/run") {
		t.Errorf("UI still references the removed /items/run endpoint")
	}
	if !strings.Contains(html, "/run`,") && !strings.Contains(html, "}/run`") {
		t.Errorf("UI no longer builds a /workflows/{name}/run POST")
	}
}
