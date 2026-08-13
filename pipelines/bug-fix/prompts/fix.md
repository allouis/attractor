Fix the bug from $context.identifier so the failing test now passes.

1. Address the **root cause** you identified, not the symptom. Keep the
   change minimal and scoped to the bug — no unrelated refactors.
2. Make the reproducing test pass, and run the surrounding tests to be
   sure you didn't break anything adjacent.

After you finish, deterministic checks run (deps, typecheck, lint, tests)
and an adversarial multi-lens review of your diff. **If you were routed
back here** because a check or the review failed, fix exactly what was
reported, then finish again.

What failed on the previous round (empty on the first visit):

---
$context.failure_reason
---

Report your outcome via `{stage_dir}/status.json`: `success` when the fix
is complete and the repro test passes, otherwise `fail` with a
`failure_reason`.
