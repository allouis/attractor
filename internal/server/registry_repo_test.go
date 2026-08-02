package server

import (
	"testing"
)

// TestSummaryIncludesRepo: a run dispatched against a registered repo
// surfaces that owner/name in its fleet summary (web-ui-v2-spec U7 repo
// provenance); a run with no repo omits the key rather than emitting "".
func TestSummaryIncludesRepo(t *testing.T) {
	reg := newRunRegistry(t.TempDir())

	withRepo := reg.NewRun("", nil, nil, reg.baseDir, nil, "", "", "owner/x", nil)
	if got := withRepo.Summary()["repo"]; got != "owner/x" {
		t.Errorf("summary repo = %v, want owner/x", got)
	}

	bare := reg.NewRun("", nil, nil, reg.baseDir, nil, "", "", "", nil)
	if _, ok := bare.Summary()["repo"]; ok {
		t.Errorf("empty repo should be omitted from summary, got %v", bare.Summary()["repo"])
	}
}

// TestRepoSurvivesReload: the repo is persisted in the manifest so a
// daemon restart reconstructs it from disk (mirrors itemRef/workflowName).
func TestRepoSurvivesReload(t *testing.T) {
	base := t.TempDir()
	reg := newRunRegistry(base)
	run := reg.NewRun("", nil, nil, reg.baseDir, nil, "", "", "owner/x", nil)
	id := run.ID

	reloaded := newRunRegistry(base)
	got, ok := reloaded.Get(id)
	if !ok {
		t.Fatalf("run %s not reloaded", id)
	}
	if repo := got.Summary()["repo"]; repo != "owner/x" {
		t.Errorf("reloaded repo = %v, want owner/x", repo)
	}
}
