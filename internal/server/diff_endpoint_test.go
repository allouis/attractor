package server

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunDiffFromJJRange drives GET /pipelines/{id}/diff (ui-tailwind-spec
// T9c): a run carrying a jj change-id range serves `jj diff --from base --to
// tip` of its cwd, so the Diff panel shows the run's real produced change
// without an uploaded *.diff artifact.
func TestRunDiffFromJJRange(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t) // foo.txt="x", @ described "init"
	base, err := jjHeadCommit(repo)
	if err != nil {
		t.Fatalf("base commit: %v", err)
	}
	// The run edits `@` in place (the default flow, no commit); the tip snapshot
	// carries the produced change. commit_id — not change_id — distinguishes it.
	if err := os.WriteFile(filepath.Join(repo, "foo.txt"), []byte("line-added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tip, err := jjHeadCommit(repo)
	if err != nil {
		t.Fatalf("tip commit: %v", err)
	}

	srv, tmp := newStageTestServer(t)
	addRun(srv, "r1", filepath.Join(tmp, "r1"))
	srv.registry.mu.Lock()
	run := srv.registry.runs["r1"]
	run.cwd = repo
	run.revBase = base
	run.revTip = tip
	srv.registry.mu.Unlock()

	resp, err := http.Get(srv.URL() + "/pipelines/r1/diff")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"foo.txt", "line-added", "+line-added"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("diff missing %q:\n%s", want, body)
		}
	}
}

// TestRunDiffDoesNotSnapshot proves GET /diff is a pure read (T9c B2): serving
// the diff must not snapshot the caller's working copy into `@`, which would be
// a write side-effect on a read that races in-progress runs and the user's own
// jj. A fresh un-snapshotted file on disk must not produce a new jj operation.
func TestRunDiffDoesNotSnapshot(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t)
	base, err := jjHeadCommit(repo)
	if err != nil {
		t.Fatalf("base commit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "foo.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tip, err := jjHeadCommit(repo)
	if err != nil {
		t.Fatalf("tip commit: %v", err)
	}
	// A dirty, un-snapshotted file: a snapshotting diff would fold it into `@`
	// and record a new operation.
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	opHead := func() string {
		out, err := exec.Command("jj", "-R", repo, "op", "log", "--no-graph", "--ignore-working-copy", "-T", "id.short()", "--limit", "1").Output()
		if err != nil {
			t.Fatalf("jj op log: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	before := opHead()

	srv, tmp := newStageTestServer(t)
	addRun(srv, "r1", filepath.Join(tmp, "r1"))
	srv.registry.mu.Lock()
	run := srv.registry.runs["r1"]
	run.cwd = repo
	run.revBase = base
	run.revTip = tip
	srv.registry.mu.Unlock()

	resp, err := http.Get(srv.URL() + "/pipelines/r1/diff")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if after := opHead(); after != before {
		t.Errorf("GET /diff snapshotted the working copy: op head %s -> %s", before, after)
	}
}

// TestRunDiffEmptyWithoutRange: a run with no recorded jj range (VM / non-jj)
// serves an empty 200 body — the frontend then falls back to a *.diff artifact.
func TestRunDiffEmptyWithoutRange(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	addRun(srv, "r1", filepath.Join(tmp, "r1"))

	resp, err := http.Get(srv.URL() + "/pipelines/r1/diff")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(body)) != "" {
		t.Errorf("no-range run should serve empty diff, got:\n%s", body)
	}
}

// TestRunDiffUnknownRun: an unknown run id is a 404, matching the other
// per-run endpoints.
func TestRunDiffUnknownRun(t *testing.T) {
	srv, _ := newStageTestServer(t)
	resp, err := http.Get(srv.URL() + "/pipelines/nope/diff")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}
