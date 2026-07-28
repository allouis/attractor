package attractor_test

import (
	"strings"
	"testing"
)

// TestServer_UI_RunPicker guards the Items run-action picker (web-ui-spec
// W4): each item row gets a run trigger that opens a picker offering the
// workflow catalog (GET /workflows) and a repo field auto-filled from the
// item's vars.repo. Structural markers; the runtime pick is driven
// separately.
func TestServer_UI_RunPicker(t *testing.T) {
	page := uiPage(t)

	for _, marker := range []string{
		`id="run-picker"`,
		`id="run-picker-workflow"`,
		`id="run-picker-repo"`,
		`id="run-picker-error"`,
		"fetchWorkflows",
		"workflowsCache",
		"/workflows",
		"openRunPicker",
		"data-run",
	} {
		if !strings.Contains(page, marker) {
			t.Errorf("page missing run-picker marker %q", marker)
		}
	}
}

// TestServer_UI_RunSubmit guards the run-action submit (web-ui-spec W4):
// confirming the picker POSTs /items/run with the item_ref + chosen
// workflow path + repo (reusing postJSON), navigates to the resulting
// #run/<id> on success, and surfaces a server error otherwise. Structural
// markers; the runtime dispatch is driven by the server-side compose test.
func TestServer_UI_RunSubmit(t *testing.T) {
	page := uiPage(t)

	for _, marker := range []string{
		"submitRun",
		"/items/run",
		"postJSON",
		"item_ref",
		"#run/",
	} {
		if !strings.Contains(page, marker) {
			t.Errorf("page missing run-submit marker %q", marker)
		}
	}
}
