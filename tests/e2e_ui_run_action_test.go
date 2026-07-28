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
