Your change did not clear review. Address the feedback below, then the
checks and review will run again.

Blocking findings from the adversarial review (empty if you were sent
here by the human at the ship gate instead):

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
point is addressed and committed, otherwise `fail` with the reason.
