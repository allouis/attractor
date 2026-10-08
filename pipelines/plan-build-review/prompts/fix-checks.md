A deterministic check failed on the change you just implemented. This
is the check's full output:

---
$context.tool.output
---

Fix the failure. Rules of engagement, same as before:

- Address exactly what the output reports — no drive-by changes.
- If the failure is environmental (a tool missing, a browser that
  cannot launch, resource exhaustion) rather than caused by the change,
  do NOT contort the code to mask it. Where you can repair the
  environment itself, do so and say what you did. Only when the checks
  cannot be made to run here at all do you report outcome `fail`.
- If the failure does NOT REPRODUCE — you re-ran it and the tree is
  green — that is a successful outcome, not a failure. You were asked to
  investigate a failing check and you established there is nothing to
  fix. Report `success` with a `notes` field saying exactly what you ran
  and what passed, and change no code. Do NOT report `fail`: a failure
  ends the whole run, discarding work that is already correct. `fail` is
  only for "these checks cannot be run in this environment".
- Commit fixes with `jj` (never `git`): small, atomic, message per the
  repo's conventions.

Report via `{stage_dir}/status.json`: outcome `success` once the fix is
committed, or once you have established there was nothing to fix:

```json
{ "outcome": "success" }
```

```json
{ "outcome": "success", "notes": "did not reproduce: re-ran the full e2e suite, 161 files / 2534 tests passed; no code changed" }
```

otherwise `fail` with the reason:

```json
{ "outcome": "fail", "failure_reason": "test runner cannot launch a browser in this environment; not fixable from the repo" }
```
