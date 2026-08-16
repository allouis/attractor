The human reviewed your plan and asked for changes before approving it.
Their note says what to address:

<feedback>
$context.human.note
</feedback>

You still have the full plan you wrote earlier in this session. Revise it
to address every point in the feedback — do not start over, and keep the
parts they did not object to.

A human re-reviews the revised plan before implementation starts. Put the
COMPLETE revised plan in your response so it is readable from the run
view.

Report via `{stage_dir}/status.json`: outcome `success` with
`context_updates` carrying `"plan_markdown"` (the COMPLETE revised plan,
verbatim markdown — this is what the implementation stage receives, in a
fresh session that sees nothing else of this conversation). For example:

```json
{
  "outcome": "success",
  "context_updates": {
    "plan_markdown": "## Scope\n...\n\n## Approach\n...\n\n## Test strategy\n...\n\n## Risks\n..."
  }
}
```

Outcome `fail` with a `failure_reason` only if the feedback cannot be
satisfied (e.g. it contradicts the issue itself).
