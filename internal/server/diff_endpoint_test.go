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
	if got := resp.Header.Get("X-Diff-Status"); got != "ok" {
		t.Errorf("X-Diff-Status = %q, want ok (direct run behavior unchanged)", got)
	}
}

// addWorkspaceRun registers a vm-placed run whose per-run jj workspace of repo
// is materialized in the shared host store, with the diff base stamped at the
// host @ the workspace is based on — the P2 shape getRunDiff resolves the tip
// from. Returns the run and the workspace path so a test can drive commits into
// it (simulating in-guest jj, which lands in the shared store).
func addWorkspaceRun(t *testing.T, srv *Server, repo, id string) (*Run, string) {
	t.Helper()
	base, err := jjHeadCommit(repo) // snapshots host @, the workspace's base
	if err != nil {
		t.Fatalf("base commit: %v", err)
	}
	work := filepath.Join(t.TempDir(), "work")
	if err := materializeWorkspace(repo, work, runWorkspaceName(id), "@"); err != nil {
		t.Fatalf("materialize workspace: %v", err)
	}
	addRun(srv, id, t.TempDir())
	srv.registry.mu.Lock()
	run := srv.registry.runs[id]
	run.cwd = repo
	run.placement = "vm"
	run.revBase = base
	srv.registry.mu.Unlock()
	return run, work
}

// commitInWorkspace writes a file in the per-run workspace and snapshots it
// (any non-ignore-working-copy jj command does), so the workspace's `@` in the
// shared host store advances — exactly what an in-guest commit does (spec W2).
func commitInWorkspace(t *testing.T, work, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("jj", "-R", work, "log", "--no-graph", "-r", "@", "-T", "commit_id").CombinedOutput(); err != nil {
		t.Fatalf("snapshot workspace: %v\n%s", err, out)
	}
}

func getDiff(t *testing.T, srv *Server, id string) (status string, body string) {
	t.Helper()
	resp, err := http.Get(srv.URL() + "/pipelines/" + id + "/diff")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	b, _ := io.ReadAll(resp.Body)
	return resp.Header.Get("X-Diff-Status"), string(b)
}

// TestRunDiffWorkspaceWithCommits: a vm run whose workspace carries in-guest
// commits serves those changes as its diff — the whole point of P2 (VM runs no
// longer stay "no diff"). Resolved LIVE (no terminal state set).
func TestRunDiffWorkspaceWithCommits(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	srv, _ := newStageTestServer(t)
	repo := jjInitRepo(t)
	_, work := addWorkspaceRun(t, srv, repo, "vm1")
	commitInWorkspace(t, work, "feature.txt", "produced-in-guest\n")

	status, body := getDiff(t, srv, "vm1")
	if status != "ok" {
		t.Errorf("X-Diff-Status = %q, want ok", status)
	}
	for _, want := range []string{"feature.txt", "produced-in-guest"} {
		if !strings.Contains(body, want) {
			t.Errorf("diff missing %q:\n%s", want, body)
		}
	}
}

// TestRunDiffWorkspaceNoCommitsYet: a live vm run whose workspace has no changes
// yet is computable-but-empty — "no commits yet", distinct from "not computable"
// (P2: distinguished by a typed field, not by sniffing an empty body).
func TestRunDiffWorkspaceNoCommitsYet(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	srv, _ := newStageTestServer(t)
	repo := jjInitRepo(t)
	addWorkspaceRun(t, srv, repo, "vm1")

	status, body := getDiff(t, srv, "vm1")
	if status != "no-commits-yet" {
		t.Errorf("X-Diff-Status = %q, want no-commits-yet", status)
	}
	if strings.TrimSpace(body) != "" {
		t.Errorf("no-commits-yet run should serve empty body, got:\n%s", body)
	}
}

// TestRunDiffWorkspaceCancelledStillServes: the diff is independent of the run's
// terminal outcome — a cancelled/crashed vm run whose workspace still exists
// serves whatever commits it produced before dying (P2 requirement).
func TestRunDiffWorkspaceCancelledStillServes(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	srv, _ := newStageTestServer(t)
	repo := jjInitRepo(t)
	run, work := addWorkspaceRun(t, srv, repo, "vm1")
	commitInWorkspace(t, work, "partial.txt", "work-before-crash\n")
	run.mu.Lock()
	run.status = RunFailed
	run.mu.Unlock()

	status, body := getDiff(t, srv, "vm1")
	if status != "ok" {
		t.Errorf("X-Diff-Status = %q, want ok (crashed run still serves its diff)", status)
	}
	if !strings.Contains(body, "work-before-crash") {
		t.Errorf("diff missing the pre-crash change:\n%s", body)
	}
}

// TestRunDiffWorkspaceForgotten: once the reaper GCs a run it forgets the
// workspace, so its tip is no longer resolvable — the diff degrades cleanly to
// "not computable" rather than erroring (P2 cleanup interaction).
func TestRunDiffWorkspaceForgotten(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	srv, _ := newStageTestServer(t)
	repo := jjInitRepo(t)
	_, work := addWorkspaceRun(t, srv, repo, "vm1")
	commitInWorkspace(t, work, "partial.txt", "produced\n") // real work, then GC forgets it
	if err := forgetWorkspace(repo, runWorkspaceName("vm1")); err != nil {
		t.Fatalf("forget workspace (reaper GC): %v", err)
	}

	status, body := getDiff(t, srv, "vm1")
	if status != "not-computable" {
		t.Errorf("X-Diff-Status = %q, want not-computable (workspace GC'd)", status)
	}
	if strings.TrimSpace(body) != "" {
		t.Errorf("GC'd run should serve empty body, got:\n%s", body)
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
	if got := resp.Header.Get("X-Diff-Status"); got != "not-computable" {
		t.Errorf("X-Diff-Status = %q, want not-computable (non-jj / no range)", got)
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

// TestJJDiffCapsOutput proves the daemon never buffers an unbounded diff (T9c
// B3): a huge produced change is read only up to the render cap (plus one byte,
// so the frontend can detect truncation) instead of blowing daemon memory.
func TestJJDiffCapsOutput(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t)
	base, err := jjHeadCommit(repo)
	if err != nil {
		t.Fatalf("base commit: %v", err)
	}
	// A file well past the cap but under jj's 1 MiB snapshot limit, so its diff
	// (~900 KiB) overruns the 512 KiB cap.
	big := strings.Repeat("some added content line\n", 40000)
	if err := os.WriteFile(filepath.Join(repo, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	tip, err := jjHeadCommit(repo)
	if err != nil {
		t.Fatalf("tip commit: %v", err)
	}

	out, err := jjDiff(repo, base, tip)
	if err != nil {
		t.Fatalf("jjDiff: %v", err)
	}
	if len(out) > maxDiffRenderBytes+1 {
		t.Fatalf("jjDiff output not capped: %d bytes (cap %d)", len(out), maxDiffRenderBytes)
	}
	if len(out) <= maxDiffRenderBytes {
		t.Fatalf("expected the huge diff to hit the cap, got only %d bytes", len(out))
	}
}
