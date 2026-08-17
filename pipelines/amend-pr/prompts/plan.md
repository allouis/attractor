This is an **existing pull request** — #$context.pr_number in
$context.repo — already checked out in your working directory; its commits
are your working copy's ancestors. The brief below is the reviewer feedback
your amendments must address (it may also carry the original issue's
context). You are amending existing work, not starting over.

<feedback>
$context.brief
</feedback>

Plan the amendments the feedback asks for. Planning only — write no code,
make no commits.

1. Understand the current state: read the PR's own change
   (`jj diff --from 'trunk()' --to @`) and the code it touches, plus the
   existing patterns and test coverage — so you plan an amendment, not a
   rewrite.
2. Write a concise amendment plan:
   - Scope: which feedback points you will address, and explicitly what is
     out of scope.
   - Test seams: the boundary each behaviour change gets tested at. Prefer
     an existing seam, and the fewest possible — ideally one. The human
     approves these at the gate, so name them plainly.
   - Slices: break the work into vertical slices, each the smallest change
     that carries its own test cycle. For each slice give the behaviour,
     the test that pins it, and the files it touches.
   - Focused-test command: the exact command that runs a single test or
     test file in this repo — implement uses it in its red/green loop.
   - Risks and open questions, if any.

A human reviews this plan before you amend — write it for them, and put
the full plan in your response so it is readable from the run view.

Report via `{stage_dir}/status.json`: outcome `success` with
`context_updates` carrying `"plan_markdown"` (the COMPLETE plan text,
verbatim markdown). plan_markdown is how the implementation stage receives
your plan — it runs in a fresh session and sees nothing else of this
conversation — so omitting it or abbreviating it breaks the pipeline. For
example:

```json
{
  "outcome": "success",
  "context_updates": {
    "plan_markdown": "## Scope\n...\n\n## Test seams\n...\n\n## Slices\n...\n\n## Focused-test command\n...\n\n## Risks\n..."
  }
}
```

Outcome `fail` with a `failure_reason` if the feedback is incomprehensible
or contradicts the PR:

```json
{ "outcome": "fail", "failure_reason": "feedback contradicts the PR's stated intent; needs a human decision" }
```
