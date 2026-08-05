package server

import (
	"fmt"
	"os/exec"
	"strings"
)

// jjHeadCommit snapshots dir's working copy and returns the immutable commit_id
// of `@`. Snapshotting is deliberate (T9c): the run's produced change lives in
// the working copy, and change_id stays constant as `@` is edited in place — so
// only the commit hash distinguishes the tree before a run from after it. The
// daemon stamps this at run start (base) and end (tip); `jj diff --from base
// --to tip` is then the run's real change. Errors when dir is not a jj repo (or
// jj is unavailable), which the caller treats as "no range".
func jjHeadCommit(dir string) (string, error) {
	cmd := exec.Command("jj", "-R", dir, "log", "--no-graph", "-r", "@", "-T", "commit_id")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("jj commit_id in %q: %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}
