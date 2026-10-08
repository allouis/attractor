Your change did not clear review. Address the feedback below, then the
checks and review will run again.

Blocking findings from the adversarial review (empty if you were sent
here by the human at the ship gate instead):

You are not obliged to comply with every finding. You can see the approved
plan and the conversation that produced it; the reviewers saw only the diff,
so they cannot tell a deliberate decision from an accident.

If a blocking finding objects to something the plan deliberately decided —
not a mistake in carrying it out, but the decision itself — do NOT change the
code to satisfy it. Say so plainly instead: name the finding, name the
decision it contradicts and where the plan states it, and explain why the
decision stands. Record that in your response so it survives into the next
round and a human can see it at the ship gate. Changing approved behaviour to
silence a reviewer is worse than leaving the finding open: it undoes a
decision a human already made, and nobody involved notices.

Where a finding is right — the code is wrong on its own terms, or does not do
what the plan decided — fix it as normal.


---
$context.failure_reason
---

The human's note (their ship-gate feedback if they requested changes;
otherwise their plan-approval note, which you have already seen):

---
$context.human.note
---

Rules of engagement:

- Respond to every blocking point: either change the code or — when you
  are confident the finding is wrong — leave it unchanged; your
  reasoning will be visible in the session for the next review round.
- Keep commits small and atomic with `jj` (never `git`), messages per
  the repo's conventions.
- Do not push; publishing happens after the ship gate.

Report via `{stage_dir}/status.json`: outcome `success` once every
point is addressed and committed:

```json
{ "outcome": "success" }
```

otherwise `fail` with the reason:

```json
{ "outcome": "fail", "failure_reason": "finding #2 demands a rewrite that conflicts with the approved plan; needs a human decision" }
```
