This is an **existing pull request** — #$context.pr_number in $context.repo —
already checked out in your working directory; its commits are the working
copy's ancestors. You are amending it, not starting over.

The feedback you must address is in the file `$context.feedback_doc`. Read it
first. Also run `gh pr view $context.pr_number --repo $context.repo --json comments,reviews`
and read any review threads — if the PR carries comments, they are feedback too.
Where the file and the PR disagree, the file wins: it is what a human wrote for
this run.

Plan the amendments. Planning only — write no code, make no commits.

## Classify every feedback point first

Amendment feedback comes in two kinds, and this pipeline handles them in two
separate phases. Before planning anything, sort each point into one:

- **CODE** — the change alters what the branch does. "This guard is in the wrong
  place", "delete this workaround", "this should stream rather than buffer".
  Produces hunks.
- **HISTORY** — the change alters how the work is arranged, not what it does.
  "Split this commit in two", "this belongs in the commit below", "the message
  describes a design that no longer exists". The tree is byte-identical before
  and after; only the commits differ.

A point that needs both is two points: say so and split it.

**The history phase runs first**, on the PR exactly as it stands, before any
code is written. That is because folding later places each hunk into the commit
that last touched those lines — so the stack must already be in its final shape,
or hunks land in commits that are about to be split out of existence.

The exception: a history point about work that does not exist yet ("put the new
helper in its own commit") cannot run first, because the helper has not been
written. Mark those **deferred** — they are carried out when the new work is
folded in, at the end.

Get this wrong and the pipeline will catch you — a history phase that changes
the tree fails, and a code phase that loses or renames an existing commit fails
— but it costs a round, so classify carefully.

## What to produce

1. **The current state.** Read the PR's own change (`jj diff --from 'trunk()' --to @`)
   and the commit stack (`jj log -r 'trunk()..@'`). You are amending a specific
   arrangement of work; know what it is.

2. **Feedback table.** Every point from the feedback, each with: its
   classification (CODE or HISTORY), what you will do about it, and — for CODE
   points — **which existing commit it belongs to**. That last column matters:
   the fold step later places hunks into commits, and your prediction is what it
   is checked against.

3. **Arrangement problems the feedback did not name.** Read the stack yourself
   and look for the ones that recur:

   - a commit that mixes a mechanical refactor with new behaviour — a refactor is
     only reviewable as a no-op when it arrives alone;
   - a commit that adds code no caller reaches until a later commit — there is no
     way to judge an interface without its use site;
   - a commit that does two unrelated things, or whose message describes only one
     of the things it does.

   Propose the splits or moves, each marked clearly as **not requested**. State
   them at the gate so a human can strike the ones they do not want, rather than
   performing them unasked. If the stack is clean on this count, say so in one
   line — do not invent work.

4. **Anything you will NOT do**, and why. A feedback point you intend to push
   back on belongs here, stated plainly, so a human can overrule you at the gate
   rather than discovering it later. Pushing back is legitimate; doing it
   silently is not.

5. **The history phase (runs first).** What splits, moves, reorders and message
   rewrites you will make, in order, and what the stack will look like when you
   are done. Name the commits by their current subject lines. List separately
   any points you are **deferring** because they concern work the code phase has
   yet to write.

6. **The code phase (runs second).** What changes you will write, as one flat
   working commit on top of the restructured stack. Do not plan to fold anything
   yet — folding happens after review, mechanically. Remember the target commits
   here are the ones that exist AFTER the history phase, so use their new names
   where a split created them.

7. **Risks.** Anything in the feedback you think is mistaken, anything that will
   be awkward to fold, any commit whose split is not clean.

Keep it short enough to read at a gate. Your final response is the plan; it is
captured verbatim and it is what the implementer works from.
