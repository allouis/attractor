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

// jjDiff renders `jj diff --from from --to to` of dir as a git-format unified
// diff (the format the UI's diffHtml parses). from/to are daemon-recorded
// commit_ids, not user input, and are passed as argv (no shell).
//
// --ignore-working-copy makes this a pure read (T9c B2): serving GET /diff must
// never snapshot the caller's working copy into `@` — that is a write
// side-effect that races in-progress runs and the user's own jj. The base/tip
// snapshots were already taken when the run was stamped.
func jjDiff(dir, from, to string) ([]byte, error) {
	cmd := exec.Command("jj", "-R", dir, "diff", "--git", "--ignore-working-copy", "--from", from, "--to", to)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("jj diff %s..%s in %q: %w", from, to, dir, err)
	}
	return out, nil
}
