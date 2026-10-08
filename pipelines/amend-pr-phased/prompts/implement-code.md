Carry out the CODE phase of the approved amendment plan.

<plan>
$context.plan_markdown
</plan>

The feedback this answers is in `$context.feedback_doc`.

## The one rule that shapes everything else

Write every change as ONE flat working commit on top of the existing stack. Do
NOT fold anything into existing commits, do not `jj squash`, do not `jj split`,
do not touch any commit that is already in the stack.

This is deliberate. The changes get reviewed before they are folded, so both the
reviewers and a human read an ordinary diff instead of a rewritten stack —
rewritten history is unreadable as a review artifact. Folding happens later,
mechanically, and is checked. Fold early and you destroy the thing being
reviewed.

For the same reason: do not restructure, reorder or re-describe commits here,
even when the plan says the history phase will. That phase comes later and a
check will fail this one if the stack shape moved.

## Doing the work

- Address the CODE points from the plan, and only those. A HISTORY point is not
  yours; leaving it undone is correct.
- Use red/green TDD where a test can drive the change.
- Keep it simple. Prefer preserving existing behaviour over improving it — an
  improvement nobody asked for is scope creep wearing a nice coat.
- Do not modify or delete a pre-existing test to make a suite pass. If an
  existing test now fails, either the change is wrong or the test encodes
  something the feedback wants changed; say which, and if it is the latter,
  explain why in your notes.
- Run focused tests as you go through a subagent, so their output does not enter
  your context: delegate "run <command> and report only the failures".

## When you are done

The working copy should contain one commit's worth of changes, described,
sitting on top of the untouched stack. Give it a message that says what the
amendments do — it is temporary and will be absorbed away, but a human reads it
at the gate.

Your final response is your build notes: what you changed and why, anything you
chose not to do and why, and anything a reviewer should look at closely. Report
via `{stage_dir}/status.json`: outcome `success` when the amendments are written
and their focused tests pass, otherwise `fail` with a `failure_reason`.
