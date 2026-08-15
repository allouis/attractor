<issue>
$context.title
$context.body
</issue>

Planning only — write no code, make no commits.

1. Build a strong understanding of the current system: read the modules
   the issue touches, the existing patterns, and the existing test
   coverage.
2. Write a concise implementation plan:
   - Scope: what the issue asks for, and explicitly what is out of scope.
   - Test seams: the boundary each new behaviour gets tested at. Prefer an
     existing seam, and the fewest possible — ideally one. The human
     approves these at the gate, so name them plainly.
   - Slices: break the work into vertical slices, each the smallest change
     that carries its own test cycle. For each slice give the behaviour,
     the test that pins it, and the files it touches.
   - Focused-test command: the exact command that runs a single test or
     test file in this repo — implement uses it in its red/green loop. (The
     full suite runs later as a pipeline check; you need only the focused
     command here.)
   - Risks and open questions, if any.

A human reviews this plan before implementation starts — write it for
them, and put the full plan in your response so it is readable from the
run view.

Report via `{stage_dir}/status.json`: outcome `success` with
`context_updates` carrying `"plan_markdown"` (the COMPLETE plan text,
verbatim markdown). plan_markdown is how the implementation stage
receives your plan — it runs in a fresh session and sees nothing else of
this conversation — so omitting it or abbreviating it breaks the
pipeline. For example:

```json
{
  "outcome": "success",
  "context_updates": {
    "plan_markdown": "## Scope\n...\n\n## Test seams\n...\n\n## Slices\n...\n\n## Focused-test command\n...\n\n## Risks\n..."
  }
}
```

Outcome `fail` with a `failure_reason` if the issue is incomprehensible
or the repo state blocks planning:

```json
{ "outcome": "fail", "failure_reason": "issue body empty and title too vague to plan from" }
```
