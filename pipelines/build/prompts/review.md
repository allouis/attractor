Review the milestone implementation against the spec `$context.spec`.

1. `jj log` + `jj diff -r <first-milestone-commit>::@-` to see the full
   change set from this run.
2. Check against `$context.spec`: does it match the milestone's scope and the
   design's architecture? Nothing missing, nothing beyond scope?
3. Check quality: tests at the right seams, race-clean, no dead code,
   honest commit messages.

Small issues: fix them yourself in additional small commits.
Fundamental problems (wrong approach, milestone half-done, build
broken): write status.json in your stage directory with {"status":
"fail", "failure_reason": "<specific findings>"} so the pipeline loops
back to implement.

If everything holds, summarise what was reviewed and confirm success.
