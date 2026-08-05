package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestJJHeadCommit reads the immutable commit_id of `@` from a real jj repo.
// It snapshots the working copy so the produced change is captured (T9c B1):
// change_id stays constant as `@` is edited, so only the commit hash tells
// before from after.
func TestJJHeadCommit(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t)
	id, err := jjHeadCommit(repo)
	if err != nil {
		t.Fatalf("jjHeadCommit: %v", err)
	}
	if len(id) < 12 {
		t.Fatalf("jjHeadCommit returned an implausible commit id %q", id)
	}

	// A non-jj directory errors — the caller treats that as "no range".
	if _, err := jjHeadCommit(t.TempDir()); err == nil {
		t.Fatal("jjHeadCommit on a non-jj dir should error")
	}
}

// TestRevRangePersistsAcrossReload proves the recorded jj rev range survives a
// daemon restart: a manifest carrying rev_base/rev_tip reloads into the run so
// the Diff endpoint still knows the range after the process that ran it is
// gone (T9c).
func TestRevRangePersistsAcrossReload(t *testing.T) {
	base := t.TempDir()
	writeManifestDir(t, base, "run1", Manifest{
		ID:      "run1",
		Status:  RunCompleted,
		RevBase: "basecommit",
		RevTip:  "tipcommit",
	})

	r := newRunRegistry(base)
	run, ok := r.Get("run1")
	if !ok {
		t.Fatal("run1 not reloaded")
	}
	if got := run.RevBase(); got != "basecommit" {
		t.Errorf("RevBase = %q, want basecommit", got)
	}
	if got := run.RevTip(); got != "tipcommit" {
		t.Errorf("RevTip = %q, want tipcommit", got)
	}
}

// TestRecordRevRangeSkipsVM proves a VM-placed run records no host jj range:
// the run executes in the guest, so the daemon's view of run.cwd is meaningless
// and must stay empty (spec: VM runs stay "no diff" until results-export).
func TestRecordRevRangeSkipsVM(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t)
	run := &Run{ID: "vm1", cwd: repo, placement: "vm", logsRoot: os.DevNull}
	run.recordRevBase()
	run.recordRevTip()
	if run.RevBase() != "" || run.RevTip() != "" {
		t.Errorf("vm run recorded a rev range: base=%q tip=%q", run.RevBase(), run.RevTip())
	}
}

// TestRecordRevRangeCapturesRunChange is the end-to-end guard the first pass
// lacked (T9c B1): the range must actually describe the run's produced change
// in the default direct/local flow, where the runner edits `@` in place with
// no `jj new`/commit. Recording the mutable change_id makes base==tip and the
// diff empty — the exact no-op T9c set out to fix. Recording the immutable
// commit_id (with a snapshot at each stamp) makes base != tip and the diff real.
func TestRecordRevRangeCapturesRunChange(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}

	cases := []struct {
		name string
		// run mutates the repo between the base and tip stamps.
		run func(t *testing.T, repo string)
	}{
		{
			name: "leave uncommitted", // agent edits @ in place, never commits
			run: func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(repo, "foo.txt"), []byte("produced-change\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "commit milestone", // agent commits, moving @ forward
			run: func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(repo, "bar.txt"), []byte("produced-change\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command("jj", "-R", repo, "commit", "-m", "milestone").CombinedOutput(); err != nil {
					t.Fatalf("jj commit: %v\n%s", err, out)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := jjInitRepo(t)
			run := &Run{ID: "r", cwd: repo, placement: "direct"}
			run.recordRevBase()
			tc.run(t, repo)
			run.recordRevTip()

			base, tip := run.RevBase(), run.RevTip()
			if base == "" || tip == "" {
				t.Fatalf("range not recorded: base=%q tip=%q", base, tip)
			}
			if base == tip {
				t.Fatalf("range collapsed (base==tip=%q): diff would be empty — the B1 no-op", base)
			}
			out, err := jjDiff(repo, base, tip)
			if err != nil {
				t.Fatalf("jjDiff: %v", err)
			}
			if !strings.Contains(string(out), "produced-change") {
				t.Errorf("diff missing the run's change:\n%s", out)
			}
		})
	}
}
