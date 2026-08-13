# A1 — Per-visit stage directories

Amends: `../attractor.md` §5.6 (Run Directory Structure).
Introduced by the local-first plan (D3, Phase 2.8), 2026-08-13.

## What the core spec says

§5.6 gives every node one stage directory:

```
{logs_root}/{node_id}/
    prompt.md
    response.md
    status.json
```

A node revisited by a retry edge overwrites its own artifacts: the
prompt/response that *caused* the loop is destroyed by the next round —
exactly the evidence needed to debug a non-converging loop.

## What we do instead

Every **visit** gets its own immutable directory (append-only history,
one of the SSSF ideas the local-first plan adopts):

```
{logs_root}/{node_id}/
    v1/               # first visit: prompt.md, response.md, status.json, tool_calls/, …
    v2/               # second visit (reached again via a retry edge)
    prompt.md         # mirror of the LATEST visit
    response.md       #   "
    status.json       #   "
```

- The engine hands each handler execution a stage dir of
  `{node_id}/v{N}` where N is the run's visit counter for that node
  (the same counter `max_node_visits` checks, and the `visit` field
  stamped on events — span identity `(node_id, visit, attempt)` needs
  no inference).
- After the visit ends (any outcome), its files are **mirrored** to the
  node root, so every §5.6 consumer — `readRecentResponses`, the
  status-file probe, stable artifact URLs — keeps working unchanged.
- Retry **attempts** within one visit share the visit dir (an attempt
  is a re-run of the same work; a visit is a re-entry of the node after
  the graph moved on and came back).
- Serving reads (daemon `/stages/{node}`, `/stages/{node}/tail`,
  `/pipelines/{id}/artifacts/{node}/{file}`) prefer the **latest v{N}**
  copy: mid-visit the root mirror is stale by definition. Stable URLs
  therefore work both mid-visit and after completion.
- `loop_restart` archives the whole tree as before (`_restart_N/`) and
  visit counting starts over — v{N} is per run incarnation.

## Why

Two dogfood failures (runs a5ac1389, 9afacdba) burned hours in
review/fix loops whose earlier rounds were unreadable: each round
overwrote the last. Opening a historical span in the waterfall and
reading exactly what the agent was asked and answered is the debugging
workflow this amendment exists for.
