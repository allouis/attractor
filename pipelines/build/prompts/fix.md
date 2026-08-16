A multi-lens code review of this milestone found blocking issues. You
are in the same working thread that planned and implemented it.

The change under review:

    jj diff --from $context.review_base --to @

The reviewers' blocking findings:

$context.failure_reason

Address **every** blocking finding:

- Fix them in small atomic jj commits, in the existing style. jj only,
  never git. Do NOT push, do NOT move bookmarks, do NOT flip the
  milestone Status.
- Keep to the milestone's scope and the spec `$context.spec` — fix the
  findings, don't gold-plate.
- TDD where a finding is a missing/weak test or a bug: add or tighten
  the test first, then make it pass.
- Write race-clean code; gofmt what you touch. If a finding is a false
  positive, say why in the commit message rather than making a
  pointless change.
- Before finishing, run the gate yourself: `nix develop -c go test
  ./... -race -count=1`.

The review will re-run on your updated change and this loop repeats
until no blocking findings remain. If the findings reveal the whole
approach is wrong (not just fixable defects), write status.json in your
stage directory with {"outcome": "fail", "failure_reason": "<why>"} so
the pipeline replans.
