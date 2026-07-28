Classify this item so the router can pick the right work pipeline.

The item's metadata is in the run context: `item.type` ($item.type),
`item.source` ($item.source), `item.id` ($item.id). The router already
tried the deterministic type routes (`pr` -> review, `issue` ->
implement) and fell through to you because the type is ambiguous.

Decide which single work pipeline this item belongs to:

- `review` — the item is a change to assess (a proposed patch, a PR-like
  artifact). No new code is expected from us.
- `implement` — the item asks us to build or change something. Code work.
- `design` — the item is underspecified: it needs a human design
  decision before any pipeline can act.

Gather just enough context to choose — read the item's title/body and,
if a repo is checked out, the immediately relevant files. Do NOT start
the work itself; you are only routing.

Report your outcome by writing `{stage_dir}/status.json` with the
chosen label in `context_updates.decision`, for example:

```json
{
  "status": "success",
  "context_updates": { "decision": "implement" },
  "notes": "Issue asks for a new flag; no design open question."
}
```

`decision` MUST be exactly one of `review`, `implement`, or `design` —
it is the routing label, matched against the router's conditional edges.
