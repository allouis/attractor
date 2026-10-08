The commit stack of a pull request has just been restructured in response to
feedback about how the work is arranged. Your job is to judge the RESULT.

There is no diff to review here. A restructuring does not change what the branch
does — that has already been asserted mechanically — so the artifact you are
reviewing is the stack itself.

Read it:

```
jj log -r 'trunk()..@'
jj diff --from <each commit>- --to <each commit>     # one commit at a time
```

The feedback that asked for this is in `$context.feedback_doc`.

Judge four things:

1. **Did it do what the feedback asked?** Go point by point through the HISTORY
   points. A split that was requested and not made, or made in the wrong place,
   is a failure. CODE points were handled in an earlier phase — ignore them.

2. **Does each commit do one thing?** A commit that needs "and" to describe it
   usually wants splitting. A commit that only makes sense alongside the next
   one usually wants merging.

3. **Does each message describe its commit?** Read the message, then the diff,
   and check they agree. A message describing a design the code no longer
   implements is the specific failure to hunt for: it survives long after the
   change that invalidated it, and it actively misleads.

4. **Could each commit stand alone?** Reading the stack bottom to top, does each
   one build on what came before without depending on what comes after? Say
   which commit breaks this if one does — splitting is where it usually goes
   wrong, because a hunk moved earlier can reference something introduced later.

Be specific: name commits by their subject line and quote what is wrong. If the
stack is right, say so plainly — a clean result is a useful result and this is
not an invitation to find something.

Report via `{stage_dir}/status.json`: outcome `success` with context_updates
{stack.summary, stack.verdict=pass} when the stack is sound; otherwise outcome
`fail` with {stack.verdict=fail, stack.summary} and a one-line failure_reason
naming what must change. Only fail for something that actually needs fixing —
a preference about arrangement that the feedback did not ask for is a note.
