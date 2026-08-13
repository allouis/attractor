# A3 — Run API surface, event kinds, parallel semantics, run.json

Amends: `../attractor.md` §9.5 (API), §9.6 (events), §4.8–4.9
(parallel/fan-in), §5.2 (outcome fields), §5.5 (artifact store), §5.6
(run dir). Records the deltas the 2026-08-13 audits found undocumented.

## §9.5 API surface (single-run server, `attractor run --ui`)

Implemented: `GET /pipelines`, `GET /pipelines/{id}` (enriched doc:
summary + spans + active nodes + pending questions + `last_seq`),
`GET /pipelines/{id}/events?since=`, `GET /pipelines/{id}/artifacts`
(+ per-file), `GET/POST questions|answer`, `GET /ui`, `GET /healthz`.

Deliberately absent (vs §9.5): `/state` and `/nodes/{node}` (folded
into the enriched doc, local-first plan D4), `POST /cancel` (stop the
process; a cancellation primitive may return with evidence),
`GET /graph`, `GET /checkpoint`, `GET /context` (read the run dir —
`/artifacts/checkpoint.json` serves the latter two). `POST /pipelines`
(submit) exists on the **hub** launcher, not on a run's own server.
`/diff` is not implemented anywhere (the local-first plan D4 text
predates this ledger entry).

## §9.6 event kinds

`InterviewCompleted` is emitted as `interview_answered`. The
`Parallel*` family is emitted as `stage_progress` events with
`Detail["kind"]`/`Detail["parallel.*"]` payloads rather than dedicated
kinds. Derived views (runview) fold on these shapes.

## §4.8/§4.9 parallel + fan-in

- Each parallel branch executes ONE node (its handler), not an
  arbitrary sub-graph walk; branches converge on their single common
  downstream node, reached via the engine-only `Outcome.NextNode`
  routing field (not in §5.2/Appendix C — engine-internal, never
  serialized).
- `wait_all` with every branch failed yields FAIL (spec pseudocode
  says PARTIAL_SUCCESS; all-failed is not partial anything).
- FanIn implements the heuristic selector only; §4.9's LLM-based
  evaluation of a `prompt` attr is not implemented.
- Each branch gets its own stage dir under the parallel node's visit
  dir (`{parallel}/v{N}/{branch}/`).

## §5.5 artifact store

Deleted (local-first plan D7 decision, resolved in the strip): the
faithful implementation had zero callers. Parallel results travel
through `parallel.results` context. Reintroduce only against evidence
of real context bloat.

## §5.6 run identity record

The run manifest is written to `run.json` (same JSON fields the spec
gives for `manifest.json`; the name predates this ledger and old run
dirs still load). Checkpoints are written only after SUCCESS-class
nodes — a resumed run re-executes a failed node from scratch rather
than resuming into its failure.

## §3.5–3.6 retry classification

The `should_retry` predicate is realized as backend-boundary
classification (`internal/backend/classify.go`): transient transport /
429 / 5xx / stall errors surface as RETRY outcomes; auth/config/
validation fail immediately. Presets beyond none/standard and the
`retry_policy` selector are not implemented;
`internal.retry_count.<node>` context keys are not set.
