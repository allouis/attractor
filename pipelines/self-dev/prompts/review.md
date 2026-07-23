Review the milestone implementation from the previous stages.

1. `jj log` + `jj diff -r <first-milestone-commit>::@-` to see the
   full change set (everything committed this run).
2. Check against `docs/service-spec.md`: does the implementation
   match the milestone's scope? Nothing missing, nothing beyond it?
3. Check quality: tests at the right seams, no dead code, comments
   only where behaviour is surprising, commit messages honest.

Small fixable issues: fix them yourself, in additional small commits.
Fundamental problems (wrong approach, milestone half-done): write
status.json in your stage directory with {"status": "fail",
"failure_reason": "<specific findings to address>"} so the pipeline
loops back to implement.

If everything holds, summarise what was reviewed and confirm success.
