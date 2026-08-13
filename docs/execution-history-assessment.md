> **Outcome (2026-08-13):** implemented, with one design change — the
> nested `{node}/v{N}/a{M}/` layout proposed here was replaced by flat
> first-class span dirs `{node}@v{visit}.a{attempt}/` at the run root,
> and parallel branches now run through the same engine executor as
> every other node. See
> [docs/spec/amendments/span-dirs.md](./spec/amendments/span-dirs.md) (A4).

# Execution History and Handoff Assessment

Date: 2026-08-13

## Purpose

This document reassesses Attractor's execution-history, stage-file, and
cross-stage handoff model after the codebase was stripped down to
self-contained runs plus an optional pull-based hub.

It is intended as a handoff to another implementation agent. The most useful
next step is a small, focused change to make persisted stage files line up with
the execution identity already present in `events.jsonl`.

## Executive summary

The architectural strip removed most of the infrastructure that previously
made artifact handling complicated. A run now owns its state locally, and the
hub archives that run directory without maintaining a second live copy.

The core observability gap remains:

> Attractor records execution spans as `(node_id, visit, attempt)`, but stores
> stage files only at `(node_id, visit)`. The waterfall also opens the node's
> latest mirrored files rather than the files belonging to the selected span.

Consequently, graph revisits are preserved, but engine retry attempts within a
visit overwrite one another. Historical spans can also display evidence from a
later visit.

The recommended solution is deliberately small:

1. Keep `{node_id}/` as the spec-compatible latest view.
2. Keep `v{visit}` to represent graph re-entry.
3. Add `a{attempt}` beneath each visit.
4. Persist a canonical `status.json` for every completed attempt, including
   failures and retries.
5. Make waterfall spans load their exact visit and attempt directory.
6. Restore visit numbering when resuming from a checkpoint.

Do not reintroduce the deleted global artifact store without evidence that
stage-local files and context references are insufficient.

## Current architecture

The unit of execution is a self-contained run. Its durable record is the run
directory:

```text
run.json
events.jsonl
checkpoint.json
<node>/v1/
<node>/v2/
<node>/prompt.md
<node>/response.md
<node>/status.json
```

`events.jsonl` is the source of truth for lifecycle and span reconstruction.
The single-run API and waterfall are derived from it. The optional hub pulls
live run state and archives the entire run directory at completion.

Relevant implementation locations:

- `internal/engine/run.go` — traversal, visits, attempts, checkpoints, stage
  allocation, mirrors.
- `internal/engine/events.go` — event schema, including `Visit` and `Attempt`.
- `internal/handler/handler.go` — Codergen prompt, response, and status-file
  handling.
- `internal/handler/contract.go` — same-session contract corrections.
- `internal/handler/tool.go` — `stdout.txt` and `stderr.txt` handling.
- `internal/runview/spans.go` — folds events into `(node, visit, attempt)`
  spans.
- `internal/runserver/server.go` — run artifact browsing and latest-visit
  fallback.
- `internal/webui/waterfall.html` — span rendering and detail lookup.
- `docs/spec/amendments/visit-dirs.md` — current per-visit layout contract.
- `docs/spec/amendments/api-and-runtime.md` — current runtime deviations,
  including deletion of the global artifact store.

## What already works

### Graph revisits have independent directories

The engine increments a node's visit count every time graph traversal re-enters
that node. Each visit receives:

```text
{logs_root}/{node_id}/v{visit}/
```

This correctly distinguishes:

```text
implement visit 1
implement visit 2
```

The node root mirrors the latest visit so existing consumers can continue to
read:

```text
implement/prompt.md
implement/response.md
implement/status.json
```

The authoritative historical files remain under `v1`, `v2`, and so on.

### Events already have the right identity

The event schema contains structured fields:

```json
{
  "node_id": "implement",
  "visit": 2,
  "attempt": 1
}
```

`internal/runview/spans.go` uses `(node_id, visit, attempt)` as span identity.
The waterfall displays those values.

Do not replace this structured identity with a parsed flat string such as
`implement_2_1`. A display label like `implement@v2.a1` is fine, but the
canonical identity should remain structured because node IDs may contain dots,
underscores, subgraph prefixes, or numeric suffixes.

### Contract corrections have separate files

Same-session corrections currently write:

```text
correction-1.prompt.md
correction-1.response.md
correction-2.prompt.md
correction-2.response.md
```

That is sufficient for now. A full `turns/t1`, `turns/t2` hierarchy is not
required for the first implementation.

### The hub archives the run directory directly

There is no longer a phone-home replication or VM stage-upload problem. If the
run directory is complete, the archive is complete. There is no need to design
around multiple live writers or transport-specific artifact synchronization.

## Remaining problems

### 1. Retry attempts overwrite one another

`executeNodeWithRetry` constructs `HandlerEnv.Stage` before entering the
attempt loop. Every attempt in one visit therefore receives the same stage
directory.

Current identity and storage do not line up:

```text
event: implement visit 1 attempt 1
files: implement/v1/

event: implement visit 1 attempt 2
files: implement/v1/       # same directory
```

The Codergen handler removes or overwrites:

```text
status.json
prompt.md
response.md
```

The Tool handler recreates:

```text
stdout.txt
stderr.txt
```

Therefore attempt 2 destroys attempt 1's verbatim stage evidence even though
both attempts remain visible in `events.jsonl`.

### 2. Waterfall details load latest files

The waterfall correctly labels a selected span with visit and attempt, but its
detail function requests:

```text
/artifacts/{node_id}/status.json
/artifacts/{node_id}/prompt.md
/artifacts/{node_id}/response.md
```

Those paths refer to the latest root mirror. Clicking an early span can display
files from a later visit.

The run server already supports arbitrary relative paths, so visit-specific
lookup can use:

```text
/artifacts/{node_id}/v{visit}/...
```

After attempt directories exist, the exact lookup should be:

```text
/artifacts/{node_id}/v{visit}/a{attempt}/...
```

### 3. Failed outcomes do not always get canonical status files

The engine currently writes its finalized `status.json` only for
`SUCCESS`-class outcomes. A failed tool, backend error, handler panic,
uncorrected contract violation, or retry attempt may have no durable canonical
status document.

The failure remains in the event log, but the attempt directory is incomplete.

Every completed attempt should receive an engine-resolved `status.json`,
including:

- `success`
- `partial_success`
- `skipped`
- `retry`
- `fail`

There is a mixed-writer issue: an agent may write `status.json`, and the engine
also treats that path as its canonical stage status. A clean future model is:

```text
agent-status.json    raw agent-authored report, when one exists
status.json          canonical engine-resolved Outcome
```

This separation is desirable but can be implemented after attempt directories
if keeping the first change smaller is important.

### 4. Resume does not preserve visit counters

The checkpoint stores completed nodes, retry counts, context, and node outcomes,
but not visit counts. Loading a checkpoint initializes the visit map empty.

This can cause resumed execution to reuse `v1` naming in an existing run
directory.

Preferred fix: derive the highest visit per node by folding `events.jsonl`
during resume. This matches the codebase's preference for derived state.

Alternative: add `NodeVisits map[string]int` to `Checkpoint`.

### 5. Accepted correction response is ambiguous

The initial response is written to `response.md`. Correction responses are
written separately, but `response.md` is not necessarily replaced by the final
accepted correction response.

The contract should explicitly choose one meaning:

- `response.md` is the initial response; or
- `response.md` is the final accepted response.

For operator expectations and spec compatibility, final accepted response is
probably the better meaning. The initial response can be retained separately
when corrections occur.

## Recommended directory layout

Use a nested, spec-compatible layout:

```text
implement/
    prompt.md                  latest compatibility mirror
    response.md
    status.json

    v1/
        prompt.md              optional latest-attempt mirror
        response.md
        status.json

        a1/
            prompt.md
            response.md
            status.json
            agent-status.json  optional future separation
            correction-1.prompt.md
            correction-1.response.md
            stdout.txt
            stderr.txt
            tool_calls/

        a2/
            ...

    v2/
        a1/
            ...
```

Semantics:

- **Visit** — graph traversal left a node and later re-entered it.
- **Attempt** — the engine cold-retried the same node visit after `RETRY`.
- **Correction** — another turn in the same backend session and attempt to fix
  a contract violation.

Example identities:

```text
implement@v1.a1                 first execution
implement@v1.a1 correction 1    same-session correction
implement@v1.a2                 cold retry
implement@v2.a1                 graph routed back to implement
```

### Why not flat directories

A layout such as:

```text
implement_1_1/
implement_1_2/
implement_2_1/
```

is not closer to the pristine spec. The spec expects
`{logs_root}/{node_id}/prompt.md`, `response.md`, and `status.json`.

Nested history preserves that stable node directory while extending it.
It also avoids parsing dynamic directory names and naturally groups a node's
execution history. The run server and hub archive both support nested files, so
there is no remaining transport reason to flatten the tree.

## Minimal implementation plan

### M1 — Attempt directories

Change stage allocation so each handler attempt receives:

```text
{node_id}/v{visit}/a{attempt}/
```

Likely implementation shape:

1. Change `stageStore(nodeID, visit)` or add an `attemptStore` helper.
2. Construct `HandlerEnv.Stage` inside the retry loop, after `attempt` is known.
3. Preserve node metadata, context, fidelity, thread, preamble, and cwd while
   replacing only the stage seam per attempt.
4. After an attempt terminates, optionally mirror it into the `v{visit}` root.
5. After the visit terminates, mirror the visit into the `{node_id}` root.

Acceptance:

- Attempt 1 and attempt 2 both retain their prompt/response/output files.
- Existing `{node_id}/prompt.md` callers still see the latest visit and attempt.
- Existing single-attempt runs remain easy to inspect.

### M2 — Canonical status per attempt

Write the engine-resolved `Outcome` to the attempt's `status.json` before
returning from every terminal attempt branch.

Acceptance:

- A failed tool has `status.json` with failure reason and context updates.
- A retry attempt has `status.json` with `outcome=retry` before the next attempt.
- An exhausted retry has a final failed attempt status.
- A handler panic has a failed status document.

Be careful not to lose the raw agent-authored status if the engine overwrites
the same path. Either defer that separation explicitly or introduce
`agent-status.json` in the same milestone.

### M3 — Exact waterfall details

Change `openDetail(s)` to use the selected span identity:

```text
/artifacts/{node_id}/v{visit}/a{attempt}/{file}
```

Acceptance:

- Clicking two visits of the same node shows different files.
- Clicking two attempts in one visit shows different files.
- Archived hub views behave identically to live run views.

### M4 — Resume visit reconstruction

When resuming, derive per-node maximum visit numbers from `events.jsonl` and
seed `state.visits` before continuing.

Acceptance:

- A resumed node does not overwrite an existing `v1` or later visit.
- The first new visit number is greater than every historical visit for that
  node in the current run incarnation.

### M5 — Tests and documentation

Add focused tests for:

- Two attempts within one visit preserve both directories.
- Retry attempt status files exist.
- Failed tool and Codergen outcomes have canonical status files.
- Waterfall detail URLs include visit and attempt.
- Resume continues visit numbering.
- Existing per-visit and root-mirror tests remain green.

Update:

- `docs/spec/amendments/visit-dirs.md`
- `README.md` run-directory example
- Any API text that describes historical stage addressing

## Data passing after the strip

The strip did not materially change cross-stage handoff. Attractor still uses:

1. Shared string context.
2. `Outcome.ContextUpdates` and agent-authored status.
3. `response.md` and synthesized fidelity preambles.
4. Optional same-thread agent-session reuse.
5. The mutable repository working tree.
6. `parallel.results` for branch outcomes.

### Appropriate context data

Keep small control values in context:

- identifiers
- routing values
- verdicts
- revision IDs
- URLs
- short summaries
- failure reasons
- human notes
- configuration inputs

### Current context risk

`output_key` copies a full untruncated response into context. The parallel
review pipeline then serializes all branch outcomes, including those full
responses, into `parallel.results`.

This can eventually bloat:

- memory
- `checkpoint.json`
- downstream prompts
- compact/summary fidelity preambles
- archived run state

However, the global artifact store was deliberately deleted because it had no
callers. Do not recreate it speculatively.

A suitable incremental approach is:

1. Measure serialized context size when writing checkpoints.
2. Add an observability warning at a meaningful threshold.
3. If a real pipeline exceeds it, store the large value as a stage-local file
   and put its relative path in context.

For example:

```text
review.correctness.ref = review_fan_out/v1/a1/correctness/response.md
```

No global artifact IDs, hashes, media-type registry, or dedicated artifact
service are needed until a concrete use case demands them.

## What not to build now

Defer the following:

- A global artifact store.
- Content-addressed artifact IDs.
- Media-type manifests.
- A dedicated artifact-write tool for agents.
- Typed non-string context.
- Automatic workspace diff manifests.
- A general turn-directory hierarchy.
- Flat execution directories.
- Any VM or phone-home-specific synchronization mechanism.

These may become useful, but none is necessary to close the current mismatch
between observable spans and persisted evidence.

## Recommended priority

### Do now

1. Attempt directories.
2. Canonical status for every attempt.
3. Exact waterfall span-to-files lookup.
4. Resume-safe visit numbering.
5. Focused e2e coverage.

### Do soon

1. Preserve raw agent status separately from canonical engine status.
2. Define `response.md` as the final accepted response.
3. Rename correction event metadata from overloaded `attempt` to `turn` or
   `correction`.
4. Verify parallel branch stage paths map correctly to branch spans.

### Defer pending evidence

1. Stage-local declared task artifacts.
2. Context-size warnings and file-reference promotion.
3. Workspace revision/diff capture.
4. Rich typed artifact contracts.

## Definition of success

After the recommended work, every waterfall span should have a stable,
permanent, exact evidence directory:

```text
(node_id, visit, attempt)
    -> prompt
    -> response
    -> resolved outcome
    -> stdout/stderr
    -> tool calls
    -> correction turns
```

Selecting an old span must never display files from a newer execution. The
same guarantee must hold while the run is live, after completion, and when
served from a hub archive.

