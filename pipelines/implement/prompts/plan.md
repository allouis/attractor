Plan the change issue $context.identifier — "$context.title" — asks for,
in $context.repo. Planning only: write no code, make no commits.

The issue is at $context.url. The repo is checked out in your working
directory. The issue body as fetched at dispatch time (may be empty —
if so, work from the title and say so in the plan):

---
$context.body
---

Reviewer feedback from the human's last look at this plan (empty on the
first pass; on a revise it says *why* the previous plan was rejected —
address it directly in the new plan):

---
$context.human.note
---

1. Record the review base **before anything else**: run
   `jj log -r @ --no-graph -T change_id` and keep the result — you will
   report it as `review_base`. The final review diffs everything from
   this change id, so it must be captured before any commit exists.
2. Read the issue body above carefully (do not assume you can reach the
   issue tracker from this environment). Then read the code it touches: find the
   relevant modules, existing patterns, and existing test coverage.
3. Write a concise implementation plan:
   - Scope: what the issue asks for, and explicitly what is out of scope.
   - Approach: the files/modules you expect to touch and how; prefer
     extending existing patterns over inventing new ones.
   - Test strategy: which tests pin the new behaviour.
   - Risks and open questions, if any.

A human reviews this plan before implementation starts — write it for
them, and put the full plan in your response so it is readable from the
run view.

Report via `{stage_dir}/status.json`: outcome `success` with
`context_updates` `{"review_base": "<the change id from step 1>"}`;
outcome `fail` with a `failure_reason` if the issue is incomprehensible
or the repo state blocks planning.
