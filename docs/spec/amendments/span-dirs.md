# A4 — Spans are first-class: one executor, one storage rule

Amends: `../attractor.md` §5.6 (Run Directory Structure), §4.8–4.9
(parallel execution), §4.7 (retries).
Supersedes: [visit-dirs.md](./visit-dirs.md) (A1).
Introduced 2026-08-13.

## The idea

A **span** is one execution attempt of one node: identity
`(node_id, visit, attempt)`. This implementation treats the span — not
the node — as the unit of execution, storage, and debugging:

- **One executor.** Every node runs through the same engine loop
  (`runNodeAttempts`): retry policy, error classification, panic
  recovery, visit counting, stage events, span storage. Parallel
  branches are not a second dialect — the parallel handler fans out via
  the engine-injected `ExecuteNode` callback, so a branch node behaves
  identically to a top-level node.
- **One storage rule.** Every span writes exactly one directory at the
  run root, named from its identity:

  ```
  {logs_root}/{node_id}@v{visit}.a{attempt}/
      prompt.md
      response.md
      status.json         # canonical, engine-resolved outcome
      agent-status.json   # the agent's verbatim self-report, if any
      tool_calls/…
  ```

  The path is **derived forward** from span identity — constructed,
  never parsed. UI, tests, and the API all build
  `{node}@v{V}.a{A}` from span data; nothing globs directories to
  discover history.

## What the core spec says

§5.6 gives every node a single `{logs_root}/{node_id}/` stage dir. A
revisited or retried node overwrites its own artifacts — destroying
exactly the evidence needed to debug a non-converging loop. §4.8 runs
parallel branches inside the parallel construct, outside the main loop.

## What we do instead

- Per-span dirs as above. Nothing is ever overwritten; every attempt —
  including failed ones — keeps its full evidence.
- `status.json` in every terminal span dir is the **engine-resolved**
  outcome (after error classification, retry decisions, tool exit-code
  mapping). If the agent wrote its own `status.json` self-report, it is
  preserved verbatim as `agent-status.json` before the canonical write.
  Debugging rule: `status.json` is what the engine decided;
  `agent-status.json` is what the agent claimed.
- No mirrors. A1's node-root mirror existed so §5.6 consumers kept
  working; all such consumers are internal and now read span data
  directly (`readRecentResponses` follows the engine's last-span
  record; the UI derives paths from span identity).
- **Resume** re-derives per-node visit counters by folding
  `events.jsonl` (max `visit` seen per node), so a resumed run
  continues numbering at max+1 instead of overwriting the crashed
  incarnation's spans. Derived state, not stored state.
- `loop_restart` archives the whole tree (`_restart_N/`) and visit
  counting restarts — span dirs are per run incarnation, as in A1.

## Why

- A1's visit dirs still shared one dir across retry **attempts** and
  still mirrored to the node root; branch nodes bypassed staging
  entirely. Three latent bugs came from that second execution dialect:
  branches had no retries (a 429'd lens silently vanished from
  `wait_all`), a branch panic killed the process, and branch visits
  were stuck at 1.
- Flat span dirs make the storage rule the same sentence as the
  identity rule. When debugging, the span is what you look at; now it
  is also what is on disk.
