# Spec amendments

[`../attractor.md`](../attractor.md) is the **pristine upstream Attractor
spec**, kept verbatim. This implementation deviates from it in a few
deliberate, documented places. Each amendment below says what the core
spec states, what we do instead, and why — and which spec §§ it touches.

Everything not listed here follows `../attractor.md` unchanged.

| Amendment | Amends (attractor.md) | Summary |
|---|---|---|
| [context-interpolation.md](./context-interpolation.md) | §4.5 (variable expansion), §9.2 (transforms) | Prompt/`tool_command` interpolation is **runtime `$context.<key>`** from the live context (§4.5's `expand_variables(prompt, graph, context)`), not a prepare-time `$var` transform. `$goal` kept as the one built-in; undefined keys fail the node. One deviation: `tool_command` context expansion (spec expands only the prompt). |
| *`output_key`* (attr) | node/graph attr table | New codergen node attr: capture the full response into `context_updates[output_key]` (used by the multi-lens review to aggregate lens findings via `parallel.results`). |
| [subgraph.md](./subgraph.md) | §4.11, §9.4, Appendix B | Static subgraph inlining (`type="subgraph"`, `graph_ref`, `var.*`) replaces the `stack.manager_loop` node type; children must be known at parse time. |
| [span-dirs.md](./span-dirs.md) | §5.6, §4.7–4.9 | Spans `(node_id, visit, attempt)` are first-class: every node — parallel branches included — runs through the one engine executor, and every attempt writes one flat dir `{node_id}@v{visit}.a{attempt}/` at the run root (canonical engine `status.json`, agent self-report preserved as `agent-status.json`, no mirrors). |
| [api-and-runtime.md](./api-and-runtime.md) | §9.5, §9.6, §4.8–4.9, §5.2, §5.5, §5.6, §3.5–3.6 | The exact single-run API surface (what's absent and why), event-kind naming, parallel/fan-in semantics (single-node branches, `Outcome.NextNode`, all-fail = FAIL), artifact-store deletion, `run.json`, success-only checkpoints, and retry classification. |

## Feature designs (not amendments)

Docs that describe additions *layered on top of* the engine — not
deviations from the spec — live in `docs/` (not here):
`provider-config.md` and the `running-pipelines.md` runbook.
