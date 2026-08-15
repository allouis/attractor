A deterministic check failed on the change you just implemented. This
is the check's full output:

---
$context.tool.output
---

Fix the failure. Rules of engagement, same as before:

- Address exactly what the output reports — no drive-by changes.
- If the failure is environmental (a tool missing, a browser that
  cannot launch, resource exhaustion) rather than caused by the change,
  do NOT contort the code to mask it: report outcome `fail` with a
  `failure_reason` saying precisely why this is not fixable from inside
  the repo.
- Commit fixes with `jj` (never `git`): small, atomic, message per the
  repo's conventions.

Report via `{stage_dir}/status.json`: outcome `success` once the fix is
committed, otherwise `fail` with the reason.
