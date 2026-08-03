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

	failed := evalUI(t, `runActionsHtml({status:'failed', workflow_name:'impl', id:'r1'})`)
	if !strings.Contains(failed, "data-rerun") || !strings.Contains(failed, "data-restart") {
		t.Errorf("failed run should offer both re-run and re-run-from-failure:\n%s", failed)
	}
	if !strings.Contains(failed, `data-run-id="r1"`) {
		t.Errorf("action buttons should carry the run id:\n%s", failed)
	}

	running := evalUI(t, `runActionsHtml({status:'running', workflow_name:'impl', id:'r1'})`)
	if strings.Contains(running, "data-rerun") || strings.Contains(running, "data-restart") {
		t.Errorf("live run should offer no re-run actions:\n%s", running)
	}

	// A raw-dot run (no workflow_name) cannot reconstruct the Run modal, so it
	// offers only re-run-from-failure.
	rawFailed := evalUI(t, `runActionsHtml({status:'failed', id:'r1'})`)
	if strings.Contains(rawFailed, "data-rerun") {
		t.Errorf("workflow-less run must not offer re-run (no modal to prefill):\n%s", rawFailed)
	}
	if !strings.Contains(rawFailed, "data-restart") {
		t.Errorf("workflow-less failed run should still offer re-run-from-failure:\n%s", rawFailed)
	}
}
