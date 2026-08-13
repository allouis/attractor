Publish the completed work for issue $context.identifier —
"$context.title" — in $context.repo as a **draft** GitHub PR. Checks and
review passed and a human approved shipping; your job is only to get the
committed work onto GitHub for their manual review.

Use `jj` for all VCS operations (never `git` directly).

1. Commit any stray working-copy changes first (small, conventional
   message) so everything ships.
2. Create a bookmark named `$context.branch` on the latest non-empty
   commit: `jj bookmark create $context.branch -r @-` — if it already
   exists (a re-run of THIS pipeline), move it with
   `jj bookmark move $context.branch --to @-` instead.
3. Push it: `jj git push --allow-new --bookmark $context.branch`.
4. Open the draft PR unless one already exists for that branch:
   `gh pr create --draft --head $context.branch --title "$context.identifier: $context.title" --body "<short summary of the change>. Refs $context.url"`
   If a PR already exists, do not create a duplicate — reuse it.
5. Do NOT mark the PR ready for review, do NOT merge, and do not push to
   any other branch.

Report via `{stage_dir}/status.json`: outcome `success` with
`context_updates` `{"pr.url": "<the PR url>"}`; outcome `fail` with a
`failure_reason` naming the exact command and error if the push or PR
creation fails.
