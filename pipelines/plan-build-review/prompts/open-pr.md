Publish the completed work as a **draft** GitHub PR. Checks and review
passed and a human approved shipping; your job is only to get the
committed work onto GitHub for their manual review.

The task this implements, for the PR description:

<brief>
$context.brief
</brief>

Use `jj` for all VCS operations (never `git` directly). `gh` targets the
repo checked out in your working directory — no `--repo` needed.

1. Commit any stray working-copy changes first (small, conventional
   message) so everything ships.
2. Push the work on an auto-named bookmark:
   `jj git push --change @-` (creates a `push-*` bookmark for the latest
   non-empty commit and pushes it; idempotent on a re-run). Then read the
   bookmark name back: `jj log -r @- --no-graph -T bookmarks`.
3. Open the draft PR unless one already exists for that bookmark:
   `gh pr create --draft --base $context.base --head <bookmark> --title "<title>" --body "<body>"`
   - `--base` targets `$context.base`, the branch the work was built and
     reviewed on.
   - Derive a concise `--title` and a `--body` from the brief and your
     diff: what changed and why, plus any issue/spec link the brief
     contains (e.g. `Refs <url>`). It is a draft for a human — a clear
     summary beats ceremony.
   - If a PR already exists for the bookmark, reuse it; do not duplicate.
4. Do NOT mark the PR ready for review, do NOT merge, and do not push to
   any other branch.

Report via `{stage_dir}/status.json`: outcome `success` with
`context_updates` carrying the PR url:

```json
{
  "outcome": "success",
  "context_updates": { "pr.url": "https://github.com/owner/name/pull/123" }
}
```

outcome `fail` with a `failure_reason` naming the exact command and error
if the push or PR creation fails:

```json
{ "outcome": "fail", "failure_reason": "`jj git push` rejected: remote denied push to protected branch" }
```
