A multi-lens code review of PR #$context.pr_number in $context.repo found
blocking issues. You are working **in the checked-out branch**
($context.bookmark) — its commits are your working copy's ancestors, and
your job is to address the findings on that branch.

The change under review:

    jj diff --from 'trunk()' --to @

The reviewers' blocking findings:

---
$context.failure_reason
---

Feedback the human left if they sent this back from the ship gate (empty
if none — treat any text here as an instruction to honour):

---
$context.human.note
---

Address **every** blocking finding:

- Read the relevant existing code before touching it — extend the branch's
  existing patterns rather than inventing new ones.
- Fix the findings in small, atomic `jj` commits, each message following
  the repo's conventions. **jj only, never `git` directly.** Do NOT push,
  do NOT move or create bookmarks — publishing happens after the ship gate.
- Keep to the scope of the findings — fix what was reported, don't
  gold-plate.
- TDD where a finding is a missing/weak test or a bug: add or tighten the
  test first, then make it pass.
- If a finding is a false positive, say why in the commit message rather
  than making a pointless change.
- Run the project's tests and formatter before you finish.

After you finish, the deterministic checks re-run — dependency install,
typecheck, lint, and the test suite — then the review re-runs on your
updated diff, and this loop repeats until no blocking findings remain. **If
you were routed back here** by a failed check, the failing output is the
thing to fix now.

Report your outcome by writing `{stage_dir}/status.json`: `success` when
the findings are addressed and the checks pass, otherwise `fail` with a
`failure_reason` describing what blocked you.
