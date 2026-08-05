package server

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// maxDiffRenderBytes bounds how much of a run's diff the daemon buffers and the
// UI renders inline (T9c B3). It mirrors the UI's ARTIFACT_RENDER_CAP: a diff
// past this is served truncated (cap+1 bytes, enough for the frontend to detect
// the overflow and show a "too large" note) so a giant/binary diff can neither
// blow daemon memory nor freeze the browser.
const maxDiffRenderBytes = 512 * 1024

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
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// Read at most cap+1 bytes so a huge/binary diff can't blow daemon memory
	// (B3); the extra byte lets the frontend detect the overflow. Once capped,
	// kill jj rather than draining the rest.
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxDiffRenderBytes+1))
	capped := len(data) > maxDiffRenderBytes
	if capped {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, fmt.Errorf("read jj diff %s..%s in %q: %w", from, to, dir, readErr)
	}
	// A deliberate kill makes Wait report an error; only surface a real failure
	// (jj erroring before we hit the cap).
	if waitErr != nil && !capped {
		return nil, fmt.Errorf("jj diff %s..%s in %q: %w: %s", from, to, dir, waitErr, stderr.String())
	}
	return data, nil
}
