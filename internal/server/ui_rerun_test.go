package server

import (
	"strings"
	"testing"
)

// TestRunActionsHtml drives the real run-detail action renderer from the
// embedded UI (web-ui-v2-spec U6): a terminal run with a workflow offers
// re-run; a failed run additionally offers re-run-from-failure; a live run
// offers neither (cancel lives in the fleet).
func TestRunActionsHtml(t *testing.T) {
	completed := evalUI(t, `runActionsHtml({status:'completed', workflow_name:'impl', id:'r1'})`)
	if !strings.Contains(completed, "data-rerun") {
		t.Errorf("completed run should offer re-run:\n%s", completed)
	}
	if strings.Contains(completed, "data-restart") {
		t.Errorf("completed run must not offer re-run-from-failure:\n%s", completed)
	}

	failed := evalUI(t, `runActionsHtml({status:'failed', resumable:true, workflow_name:'impl', id:'r1'})`)
	if !strings.Contains(failed, "data-rerun") || !strings.Contains(failed, "data-restart") {
		t.Errorf("resumable failed run should offer both re-run and re-run-from-failure:\n%s", failed)
	}
	if !strings.Contains(failed, `data-run-id="r1"`) {
		t.Errorf("action buttons should carry the run id:\n%s", failed)
	}

	// A failed run the daemon reports non-resumable (a reloaded shell or a
	// local/vm run) must NOT offer re-run-from-failure — only a fresh re-run.
	notResumable := evalUI(t, `runActionsHtml({status:'failed', resumable:false, workflow_name:'impl', id:'r1'})`)
	if strings.Contains(notResumable, "data-restart") {
		t.Errorf("non-resumable failed run must not offer re-run-from-failure:\n%s", notResumable)
	}
	if !strings.Contains(notResumable, "data-rerun") {
		t.Errorf("non-resumable failed run should still offer a fresh re-run:\n%s", notResumable)
	}

	running := evalUI(t, `runActionsHtml({status:'running', workflow_name:'impl', id:'r1'})`)
	if strings.Contains(running, "data-rerun") || strings.Contains(running, "data-restart") {
		t.Errorf("live run should offer no re-run actions:\n%s", running)
	}

	// A raw-dot run (no workflow_name) cannot reconstruct the Run modal, so it
	// offers only re-run-from-failure when resumable.
	rawFailed := evalUI(t, `runActionsHtml({status:'failed', resumable:true, id:'r1'})`)
	if strings.Contains(rawFailed, "data-rerun") {
		t.Errorf("workflow-less run must not offer re-run (no modal to prefill):\n%s", rawFailed)
	}
	if !strings.Contains(rawFailed, "data-restart") {
		t.Errorf("workflow-less resumable failed run should still offer re-run-from-failure:\n%s", rawFailed)
	}
}

// TestParseRef drives the item_ref string parser: the summary carries the
// opaque source:type:external_id tag, and re-run must rebuild the structured
// ref the Run modal links. The external id keeps any embedded ':' (join the
// remainder), and a missing/short tag yields null (a standalone re-run).
func TestParseRef(t *testing.T) {
	got := evalUI(t, `JSON.stringify(parseRef('github:pr:allouis/attractor#42'))`)
	want := `{"source":"github","type":"pr","external_id":"allouis/attractor#42"}`
	if got != want {
		t.Errorf("parseRef = %s, want %s", got, want)
	}
	if out := evalUI(t, `String(parseRef(''))`); out != "null" {
		t.Errorf("parseRef('') = %s, want null", out)
	}
	if out := evalUI(t, `String(parseRef('github:pr'))`); out != "null" {
		t.Errorf("parseRef of a short tag = %s, want null", out)
	}
}

// TestRerunOpts reshapes a run summary into the openRunForm options a re-run
// resubmits (web-ui-v2-spec U6): the workflow, the launch vars as the field
// prefill, and the linked item ref rebuilt from the tag.
func TestRerunOpts(t *testing.T) {
	got := evalUI(t, `JSON.stringify(rerunOpts({`+
		`workflow_name:'review-pr', repo:'a/b', `+
		`vars:{repo:'a/b', identifier:'ENG-42'}, `+
		`item_ref:'github:pr:a/b#1'}))`)
	for _, want := range []string{
		`"workflowName":"review-pr"`,
		`"identifier":"ENG-42"`,
		`"repo":"a/b"`,
		`"source":"github"`,
		`"external_id":"a/b#1"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rerunOpts missing %q:\n%s", want, got)
		}
	}

	// A standalone run (no item_ref) rebuilds no ref; repo falls back to the
	// summary's repo when the vars map omits it.
	standalone := evalUI(t, `JSON.stringify(rerunOpts({workflow_name:'impl', repo:'a/b', vars:{}}))`)
	if !strings.Contains(standalone, `"itemRef":null`) {
		t.Errorf("standalone rerunOpts should carry a null itemRef:\n%s", standalone)
	}
	if !strings.Contains(standalone, `"repo":"a/b"`) {
		t.Errorf("standalone rerunOpts should backfill repo from the summary:\n%s", standalone)
	}
}
