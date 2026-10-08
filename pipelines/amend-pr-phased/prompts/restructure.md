Carry out the HISTORY phase of the approved amendment plan.

<plan>
$context.plan_markdown
</plan>

The feedback is in `$context.feedback_doc`. This phase runs FIRST, on the pull
request exactly as it stands. No code amendments have been written yet, and
that is deliberate: the code phase folds its hunks into whichever commit last
touched those lines, so the stack must already be in its final shape or hunks
land in commits that are about to stop existing.

Your scope is the HISTORY points only — feedback about how existing work is
arranged rather than what it does.

A history point about work that does not exist yet — "put the new helper in its
own commit", where the code phase has yet to write that helper — is NOT yours.
Leave it; it is handled when the new work is folded in. Say in your notes that
you deferred it.

## The invariant

**This phase must not change what the branch does.** A pure restructuring leaves
`jj diff --from 'trunk()' --to @` byte-identical before and after. Only the
commits differ: their boundaries, their order, their messages.

That is checked mechanically the moment you finish, and it will fail if the tree
moved. So do not fix a bug you notice, do not tidy a line, do not improve a
name — however tempting, and even if it is obviously right. Note it in your
response instead; it can be someone's next piece of feedback.

## The work

Typical points and their tools:

- **Split a commit** — `jj split -r <rev>`, choosing which hunks go in the first
  part. Each resulting commit must be coherent on its own and get its own
  message; "part 1" and "part 2" are not messages.
- **Move a change between commits** — `jj squash --from <a> --into <b>` with a
  fileset, or `jj split` then squash.
- **Reorder** — `jj rebase -r <rev> --before/--after <rev>`.
- **Rewrite a message** — `jj describe -r <rev>`.

Work in the order the plan gives. After each step run
`jj log -r 'trunk()..@'` and confirm the stack is what you expect; a
restructuring that goes wrong halfway is much easier to understand immediately
than three operations later.

Two properties to preserve, both of which a reader of the final PR depends on:

- **Each commit builds and passes its own tests alone.** Splitting is where this
  breaks: a hunk moved earlier may reference something introduced later. If a
  split cannot preserve it, say so rather than producing a stack that only works
  at the tip.
- **Each message describes its commit's actual contents** after the split.

Report via `{stage_dir}/status.json`: outcome `success` when the plan's history
points are done and the tree is untouched, otherwise `fail` with a
`failure_reason`. If a requested split is genuinely not possible without
changing the tree, that is a `fail` with the reason — not a quiet code change.
