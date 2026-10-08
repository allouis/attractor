The approved code amendments have just been folded into the stack by
`jj absorb`, which moved every hunk whose home was unambiguous into the commit
that last touched those lines. Its output is above.

What absorb could not place, it left behind in the working commit. That
leftover set is your job: it is exactly the set that needs judgement rather than
mechanism — new files, lines no existing commit ever touched, hunks that could
plausibly belong to two commits.

## Place what is left

Run `jj st` and `jj diff` to see what remains. For each remaining change, decide
which commit it belongs in and move it there with `jj squash --into <rev>`.

The plan predicted a target commit for each feedback point:

<plan>
$context.plan_markdown
</plan>

Follow those predictions unless the code says otherwise. Where you depart from
the plan, say so and why in your notes — a mismatch between predicted and actual
placement is worth a human knowing about.

A change that genuinely belongs in none of the existing commits is the one case
where a NEW commit is correct. It must stand on its own merits — a coherent
piece of work with its own message — and go in the right position in the stack,
not simply on top. A commit whose real purpose is "the leftovers from an
amendment round" is not acceptable; that is the correction commit this whole
pipeline exists to avoid.

This is also where feedback about arranging NEW work is carried out. The history
phase ran before any of these amendments existed, so a point like "put the new
helper in its own commit" was deferred to you — the helper did not exist yet.
Check the plan for deferred history points and honour them here. Rearranging
work that already existed before this round is NOT yours: that phase is over,
and a check will fail the run if an existing commit vanishes or is renamed.

When you are finished the working copy must be empty: `jj st` shows no changes,
and every amendment lives in a commit that earns it.

## Then fix the messages

Absorb moves code; it cannot move meaning. A commit whose content changed may
now have a message describing something it no longer does — and a stale message
is worse than no message, because it tells a future reader something false long
after the review that changed it is forgotten.

Read each commit you touched and check its subject and body still describe what
it now contains. Rewrite the ones that do not with `jj describe`, including
removing names and concepts the code no longer uses. Do not rewrite messages on
commits you did not touch.

Report via `{stage_dir}/status.json`: outcome `success` when the working copy is
empty and every message matches its commit, otherwise `fail` with a
`failure_reason`. Your final response should list where each leftover hunk went
and which messages you rewrote.
