Classify this item so the router can pick the right work pipeline.

The item's metadata is in the run context: `item.type`
($context.item.type), `item.source` ($context.item.source), `item.id`
($context.item.id). A PR was already routed deterministically to review;
you are here because this is an issue (or an ambiguous type) that needs a
judgement call.

Decide which single work pipeline this item belongs to:

- `bugfix` — the item reports a **defect**: something is broken,
  regressed, throwing, or behaving wrong. The fix starts by reproducing it
  with a failing test.
- `implement` — the item asks us to **build or change** something that
  works today: a feature, an enhancement, a refactor. Not a defect.
- `review` — the item is a change to **assess** (a proposed patch or
  PR-like artifact). No new code is expected from us.
- `design` — the item is underspecified: it needs a human design decision
  before any pipeline can act.

Gather just enough context to choose — read the item's title/body and, if
a repo is checked out, the immediately relevant files. Do NOT start the
work itself; you are only routing.

Report your outcome by writing `{stage_dir}/status.json` with the chosen
label in `context_updates.decision`, for example:

```json
{
  "status": "success",
  "context_updates": { "decision": "bugfix" },
  "notes": "Report of a crash with a stack trace — a defect to reproduce."
}
```

`decision` MUST be exactly one of `bugfix`, `implement`, `review`, or
`design` — it is the routing label, matched against the router's
conditional edges.
