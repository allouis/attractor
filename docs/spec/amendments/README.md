# Spec amendments

[`../attractor.md`](../attractor.md) is the **pristine upstream Attractor
spec**, kept verbatim. This implementation deviates from it in a few
deliberate, documented places. Each amendment below says what the core
spec states, what we do instead, and why — and which spec §§ it touches.

Everything not listed here follows `../attractor.md` unchanged.

| Amendment | Amends (attractor.md) | Summary |
|---|---|---|
| [context-interpolation.md](./context-interpolation.md) | §4.5 (variable expansion), §9.2 (transforms) | Prompt/`tool_command` interpolation is **runtime `$context.<key>`** from the live context (§4.5's `expand_variables(prompt, graph, context)`), not a prepare-time `$var` transform. `$goal` kept as the one built-in; undefined keys fail the node. One deviation: `tool_command` context expansion (spec expands only the prompt). |
| [routing.md](./routing.md) *(superseded)* | §4.11 (manager loop), §5.1 (context), §9.4 (sub-pipeline nodes), node/graph attr table | Work **routing** is inline sub-pipelines: `stack.manager_loop` with per-node `stack.child_dotfile`/`child_workdir`, seeding the child's initial context (§5.1 seeded context), routed by conditional edges over static manager-loop nodes. Adds the `stack.child.var.*` attr. No `dispatch` node / `Runner` seam. |
| *`output_key`* (attr) — see [../../archive/review-pipeline-spec.md](../../archive/review-pipeline-spec.md) | node/graph attr table | New codergen node attr: capture the full response into `context_updates[output_key]` (used by the multi-lens review to aggregate lens findings via `parallel.results`). |
| [visit-dirs.md](./visit-dirs.md) | §5.6 (run directory structure) | Per-visit stage dirs: each visit writes `{node_id}/v{N}/`, the latest visit is mirrored at the node root. Revisited nodes stop destroying the evidence of earlier rounds; §5.6 consumers and stable artifact URLs keep working. |
| *API surface* — see [../../local-first-plan.md](../../local-first-plan.md) D4 | §9.5 | The enriched `GET /pipelines/{id}` absorbs the derived state document (summary + spans + active nodes + pending questions); `/state` and `/nodes/{node}` are dropped on the single-run server. Kept extensions, documented as such: `/artifacts` (run-dir file serving, implied by §5.6) and `/diff`. |

## Feature designs (not amendments)

Docs that describe additions *layered on top of* the engine — not
deviations from the spec — live in `docs/` (not here):
`local-first-plan.md`, `loop-guards-spec.md`, `provider-config.md`,
`acp-backend.md`, `codergen-backends-spec.md`. Specs for deleted
subsystems are under `docs/archive/`.
