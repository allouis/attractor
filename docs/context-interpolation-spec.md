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

## Migration sequencing (non-breaking)

The build pipeline (the tool doing this migration) is executed by the
installed `attractor` **runner** binary and itself uses `$goal`/`$spec`.
To avoid breaking the loop mid-migration:

1. **C1–C4 are additive** — runtime `$context` interpolation, seeding,
   goal, validation — while the **prepare-time transform stays**. Old
   `$var` pipelines and the stale runner keep working; `go test`
   (fresh-compiled) validates the new path.
2. **Rebuild + reinstall the runner** (`nix build .#attractor` + install)
   **before C5**, so the binary that drives the loop understands
   `$context` and seeds the CLI `-var` into context.
3. **C5 migrates every pipeline** (`review`, `implement`, `router`,
   `build`) `$x` → `$context.x` (keep `$goal`). The run that *implements*
   C5 uses the old build pipeline; the next iteration uses the new one on
   the rebuilt runner.
4. **C6** reframes R3; **C7 removes the transform** (safe — nothing uses
   `$var` any more); **C8** docs.

## Milestone ledger

| # | Milestone | Deps | Status |
|---|---|---|---|
| C1 | `expandContext` helper + codergen node expands its `prompt` from live context at execute time (spec §4.5); `$goal`→`graph.goal`; fail-fast on undefined. **Additive** — prepare-time transform still runs. | — | done |
| C2 | `tool` node expands `tool_command` from live context at execute time (the one deviation), shell-safe (`$context.*` only). | C1 | done |
| C3 | Every run path (CLI `run`, server submit, automations, cron) seeds `-var`/Item vars into `InitialContext`; `vars=` validated at run-start as required context keys (fail-fast). | — | done |
| C4 | Resolve graph `goal` once at run-start from seeded context. | C3 | done |
| C5 | Migrate all pipelines (`review`, `implement`, `router`, `build`) `$x`→`$context.x`; keep `$goal`. *(runner rebuilt first — see sequencing)* | C1, C2, C3 | done |
| C6 | Simplify R3: `manager_loop` seeds its child's initial context; child interpolates `$context.*` at runtime; drop the context→declared-vars conversion. | C1, C3 | done |
| C7 | Remove the prepare-time `VariableExpansion` transform; `-var` only seeds context. | C5, C6 | done |
| C8 | Docs: fix attractor-spec §4.5 (runtime context interpolation; drop the misleading `$goal`-only prose; note the `tool_command` deviation); update router-spec/items-spec (R3 reframe, `$context.` syntax); this spec's deviations. | C7 | todo |

## Testing conventions

- **C1**: a codergen node with `prompt="PR #$context.pr_number"` and
  context `{pr_number:42}` sends `PR #42`; an undefined `$context.x`
  fails the node naming `x`; `$goal` still resolves; `$HOME` untouched.
- **C2**: a tool node `tool_command="echo $context.repo"` with context
  `{repo:foo/bar}` echoes `foo/bar`; a literal `$HOME` in the command
  reaches the shell unexpanded.
- **C3**: `attractor run p.dot -var k=v` exposes `k` to the first node's
  `$context.k`; a pipeline declaring `vars="k"` run without `k` fails at
  start naming `k`.
- **C4**: `goal="PR #$context.pr_number"` shows resolved in the run
  summary.
- **C5**: existing pipeline e2e tests pass with `$context.` syntax.
- **C6**: the router e2e (`TestRouter_PRRoutesToReviewChild`) still
  routes a PR to the review child, child sourcing vars from seeded
  context (no prepare-time conversion).
- **C7**: no reference to `VariableExpansion` remains; `$var` (bare) no
  longer expands.

## Not in scope

- Nested-field access into JSON context values (`$context.parallel.results`
  yields the whole JSON string; the reader parses it).
- Removing `$goal` (kept as the spec built-in).
- Runtime interpolation of attrs beyond `tool_command` (add per need).
