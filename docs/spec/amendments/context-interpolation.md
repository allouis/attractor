# Context interpolation — spec (align vars → context)

Attractor invented a prepare-time **vars** system (`-var`, `vars=`,
arbitrary `$name`, attr expansion) that the core spec never had. The
spec's model is: data lives in the **Context KV store** (§5.1), and the
**codergen node interpolates the prompt from context at runtime** —
`expand_variables(prompt, graph, context)` in §4.5. This migration
replaces the invented vars map with spec-faithful runtime context
interpolation, leaving exactly **one** documented deviation:
`tool_command` (attr) expansion.

Status: **ready to build** (milestone ledger below).

## What the spec says (and what we did instead)

- **Spec §4.5**: the codergen handler does `prompt = expand_variables(
  prompt, graph, context)` at **runtime**, with the live context. Line
  707: `$goal` is the only *built-in* (graph-derived) variable; the
  `context` argument is what makes user variables resolvable. "Not a
  templating engine" = simple `$key`→value replacement.
- **Spec §9 transform**: a *prepare-time* transform expanding `$goal`
  from the graph goal.
- **Our impl**: skipped §4.5's runtime expansion; instead extended the
  §9 prepare-time transform to an arbitrary `-var`/`vars=` map and
  applied it to **all** attrs. That vars map is the invention.

The spec's runtime-context model is strictly more powerful: it works
under **all** fidelities (node-level, always runs — unlike the §5.4
preamble, which is empty under `full`), and needs **one** store.

## Locked decisions

1. **Runtime, per-node, from current context.** Each handler expands the
   attr it consumes at execute time, reading the *live* context (so both
   seeded inputs and values written by earlier nodes resolve). Shared
   `expandContext(s, ctx)` helper. This is spec §4.5 for the codergen
   `prompt`; generalised to `tool_command` (the one deviation).
2. **Syntax: `$context.<dotted.key>`.** A distinctive prefix so we touch
   *only* our placeholders and leave every other `$` for the shell
   (`$HOME`, `$(…)`) or literal prose. Match `$context.` followed by a
   key run of `[A-Za-z0-9_.]+`, trailing dots stripped, boundary at the
   first non-key char. The key is looked up **exactly** in the flat
   context store — `$context.item.type`, `$context.parallel.results`,
   `$context.stack.child.status` all resolve. `$$` is a literal `$`.
3. **`$goal` stays** — the one spec built-in (line 707) — as sugar for
   `$context.graph.goal` (goal is mirrored into context by `MirrorGraph`).
4. **Fail-fast on undefined key.** `$context.foo` with no `foo` in
   context at execute time → the node FAILs with `unresolved
   $context.foo`. `$context.` is new syntax, so nothing legacy relies on
   pass-through, and a mangled `gh` command is worse than a clear error.
5. **`-var x=y` → seed `context[x]=y`.** Same CLI ergonomics, backed by
   context. Every run path (CLI `run`, server submit, automations, cron)
   seeds `-var`/Item vars into the run's **initial context** (extends
   R2's `InitialContext`, which today only the server submit path uses).
6. **`vars=` → required context keys, validated at run-start** (after
   seeding), fail-fast. Missing *inputs* fail before any node runs;
   genuinely mid-run keys fail at the referencing node (decision 4).
7. **Graph `goal` resolved once at run-start** from the seeded context
   (it is display text + the `$goal` source), then frozen.
8. **Drop the prepare-time `VariableExpansion` transform** — once
   pipelines are migrated (last, so the migration stays non-breaking).

## The one deviation

| | Spec | This design |
|---|---|---|
| codergen `prompt` | runtime expand from context (§4.5) | **matches** |
| `tool_command` (and parameterised tool attrs) | *not expanded* — §4.5 expands only the prompt | **DEVIATION**: expand `$context.*` from context at runtime, shell-safe (leaves `$HOME`/`$(…)`) |

**Deviations this migration REMOVES** (net simpler than today): the
invented `-var`/`vars=` map, arbitrary prepare-time `$name` expansion,
and prepare-time expansion of *all* attrs. Only `tool_command` context
expansion remains as a named, minimal deviation.

## Design detail

- **`expandContext(s string, ctx *Context) (string, error)`** — the
  shared helper. Walks `s`, replaces each `$context.<key>` with
  `ctx.Get(key)`, errors on a missing key (naming it), passes every
  other byte through untouched (`$$` → `$`).
- **Codergen** (`handler.go`): `prompt = expandContext(node.Prompt(),
  ctx)` before prepending the preamble / calling the backend (spec §4.5
  placement). `$goal` resolves via the `graph.goal` context key.
- **Tool** (`tool.go`): `cmd = expandContext(node.Attrs["tool_command"],
  ctx)` before `/bin/sh -c`. Only `$context.*` is touched; shell syntax
  survives.
- **Run-start** (engine): after seeding `InitialContext` and
  `MirrorGraph`, (a) resolve `$context.*` / `$goal` in the graph `goal`
  attribute from the initial context; (b) validate every `vars=`
  declared key is present in the initial context, else fail the run with
  a clear "missing required input" error.
- **Preamble** (§5.4) is unchanged and complementary — inline
  `$context.*` is explicit substitution; the preamble is grounding/
  history. Both can coexist.

## Not in scope

- Nested-field access into JSON context values (`$context.parallel.results`
  yields the whole JSON string; the reader parses it).
- Removing `$goal` (kept as the spec built-in).
- Runtime interpolation of attrs beyond `tool_command` (add per need).
