Implement the change issue $context.identifier — "$context.title" — asks for, in $context.repo.

The issue is at $context.url. The repo is checked out in your working
directory. The issue body:

---
$context.body
---

A planning stage already ran and a human approved its plan — follow it;
if reality forces a deviation, note it in your response. The approved
plan:

---
$context.plan_markdown
---

Reviewer feedback the human left when approving the plan (empty if they
approved without a note — treat any text here as an instruction to honour
while implementing):

---
$context.human.note
---

1. Read the issue to understand the intended change and its acceptance
   criteria. Read the relevant existing code before touching anything —
   prefer extending existing patterns over inventing new ones.
2. Make the change test-first where practical: add or adjust tests that
   pin the behaviour, then make them pass. Keep the change scoped to
   what the issue asks — nothing missing, nothing beyond.
3. **Commit as you go with `jj`** (never `git` directly): small, atomic,
   reviewable commits, each message following the repo's conventions.
   Do not push — publishing happens after the ship gate.
4. Run the project's tests and formatter before you finish.

After you finish, deterministic checks run — dependency install,
typecheck, lint, and the test suite — followed by an adversarial
multi-lens review of your diff. **If you were routed back here** because
a check or the review failed, the failing output / findings are the
thing to fix now: address exactly what was reported, then finish again.

Report your outcome by writing `{stage_dir}/status.json`: `success`
when the change is complete and the tests pass, otherwise `fail` with a
`failure_reason` describing what blocked you.
