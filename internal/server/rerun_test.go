package server

import (
	"testing"

	"github.com/allouis/attractor/internal/items"
	"github.com/allouis/attractor/internal/items/source"
)

// TestRunSummary_ExposesLaunchVars proves the run summary carries the declared
// vars a manual re-run resubmits (web-ui-v2-spec U6): the run's launch vars,
// with the seeded item.* metadata and static check.* commands filtered out so
// the Run modal prefills only what the human filled in.
func TestRunSummary_ExposesLaunchVars(t *testing.T) {
	catalog := t.TempDir()
	writeVarsWorkflow(t, catalog, "review-pr", "repo,pr_number,title")
	repoDir := t.TempDir()
	ref := items.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"}
	fs := &fakeSource{getItem: source.Item{
		Ref:   ref,
		Title: "Fix login",
		Vars: map[string]string{
			"repo":      "allouis/attractor",
			"pr_number": "42",
			"title":     "old title",
		},
	}}
	srv := runFormServer(t, catalog, map[string]source.Source{"github": fs}, items.Repos{"allouis/attractor": repoDir})

	_, id := postRunWorkflow(t, srv.URL(), "review-pr", map[string]any{
		"item_ref": ref,
		"vars":     map[string]string{"title": "new title"},
	})
	run, ok := srv.registry.Get(id)
	if !ok {
		t.Fatalf("run %q not registered", id)
	}
	vars, ok := run.Summary()["vars"].(map[string]string)
	if !ok {
		t.Fatalf("summary vars = %v, want a map[string]string", run.Summary()["vars"])
	}
	want := map[string]string{"repo": "allouis/attractor", "pr_number": "42", "title": "new title"}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("summary vars[%q] = %q, want %q", k, vars[k], v)
		}
	}
	for k := range vars {
		if k == "" || len(k) >= 5 && (k[:5] == "item." || k[:5] == "check") {
			t.Errorf("summary vars leaked seeded key %q", k)
		}
	}
	pollRunSummary(t, srv.URL(), id) // let the run finish before tempdir cleanup
}
