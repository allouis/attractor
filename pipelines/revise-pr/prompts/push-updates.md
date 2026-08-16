Push the fixes you made onto the existing PR branch for PR
#$context.pr_number in $context.repo. The branch — bookmark
`$context.bookmark` — already exists on the remote; pushing it updates the
PR **in place**. Checks and review passed and a human approved shipping.

Use `jj` for all VCS operations (never `git` directly).

1. Commit any stray working-copy changes first (small, conventional
   message) so everything ships.
2. Make sure the bookmark points at your latest commit — move it if a fix
   commit landed after it: `jj bookmark move $context.bookmark --to @-`
   (skip if it is already there).
3. Push the existing bookmark, and only that bookmark:

       jj git push --bookmark $context.bookmark

   The bookmark already exists on the remote, so do NOT pass `--allow-new`.

Do **only** the push. Explicitly do NOT:

- create a PR (`gh pr create`) — the PR already exists and updates from the
  push;
- post any PR comment or review, mark the PR ready/merged, or change its
  state;
- push any other bookmark or branch, or move the trunk.

Report via `{stage_dir}/status.json`: outcome `success` with
`context_updates` `{"pushed": "true"}` once the push succeeds:

```json
{
  "outcome": "success",
  "context_updates": { "pushed": "true" }
}
```

outcome `fail` with a `failure_reason` naming the exact command and error
if the push fails:

```json
{ "outcome": "fail", "failure_reason": "`jj git push --bookmark eng-42` rejected: non-fast-forward (remote moved)" }
```
