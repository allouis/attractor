# Router — spec (Phase 2 of items-spec)

Routing = "given an Item, pick the workflow and run it." Attractor
already has the composition primitive for this: the **sub-pipeline
node** (attractor-spec §9.4), of which `stack.manager_loop` is the
worked example. A *router* is an ordinary graph whose conditional edges
select among static `manager_loop` nodes — one per target pipeline. No
new node type, no `Runner` seam, no first-class child run.

This **supersedes** the earlier "dispatch node + `Runner` seam" design
(items-spec decisions 4/6/7, and the deleted `dispatch-spec.md`). That
approach produced first-class, independently-listable child runs at the
cost of a new engine seam, `item_ref` threading through the engine, and
two in-progress runs per item. Running the child **inline** (the spec's
§9.4 model) drops all of that. The trade: the work runs *nested inside*
the router run rather than as a sibling — visible via the router run's
telemetry (`stack.child.*`), surfaced in the UI as the deepest
executing graph.

Status: **ready to build** (milestone ledger below).

## Shape

```
POST /items/dispatch {item_ref}
  → daemon resolves Item → vars, seeds them into the router run's
    initial context, stamps item_ref (reuses the I4 submit path)
  → router graph runs:
        classify ─[conditional edges on context]─▶ manager_loop(child="review.dot")
                                                 ─▶ manager_loop(child="implement.dot")
                                                 ─▶ needs_design (surface to human)
  → chosen manager_loop runs its child pipeline INLINE, supervises,
    branches on the child's outcome
```

One registry run per dispatch (the router). It carries `item_ref`. The
work is a sub-pipeline nested inside it.

## Design

### Routing is edges, not attributes

The router graph declares its targets **statically** — one
`manager_loop` node per target pipeline — and routes with conditional
edges (attractor-spec §10, evaluated against runtime context):

```dot
digraph router {
    classify [type="conditional"]
    classify -> review_loop    [condition="item.type == pr"]
    classify -> implement_loop [condition="item.type == issue"]
    classify -> triage         [condition="item.type == unknown"]

    // agent fallback for the ambiguous cases → writes `decision` to context
    triage [prompt="@prompts/triage.md"]
    triage -> review_loop    [condition="decision == review"]
    triage -> implement_loop [condition="decision == implement"]
    triage -> needs_design   [condition="decision == design"]

    review_loop    [type="stack.manager_loop", stack.child_dotfile="review.dot"]
    implement_loop [type="stack.manager_loop", stack.child_dotfile="implement.dot"]
    needs_design   [type="tool", tool_command="..."]   // surface to a human
}
```

Route targets are a **closed set** declared in the graph — explicit,
reviewable, visible in the DOT. The agent fallback emits a *label*
within that set; it never conjures an arbitrary pipeline path. Adding a
target = adding a node + edge (a feature, not friction).

Attribute-based dynamic selection (`stack.child_dotfile` resolved from
context) was rejected: attractor has no runtime attribute interpolation
(prompt/attr expansion is prepare-time `$var` substitution — "not a
templating engine"), so a dynamic attr would be a *new* engine
capability. Conditional edges are the spec's existing branching
mechanism; use them.

### Child pipeline gets its vars from context

Attractor has two value systems:

- **prepare-time `$vars`** — a pipeline's external inputs, string-
  substituted into the graph before the run (`setup.Prepare`).
- **runtime context** — the KV store handlers read/write during the run
  (attractor-spec §5.1 / §11.7).

Handlers do **not** interpolate context into attrs/prompts: the `tool`
handler runs `tool_command` verbatim (`/bin/sh -c`), and prompt
expansion is simple `$goal` replacement ("not a templating engine").

The child pipeline (`review.dot`) is authored against the prepare-time
contract — it declares `vars="repo,pr_number,url,title"` and runs
standalone as `attractor run review -var pr_number=42`. So
`manager_loop` supplies those `-var`s **from context** at child-prepare
time — doing programmatically what a human types on the CLI:

```
for name in child.declared_vars:        // the child's `vars="..."` list
    vars[name] = context.get(name)
merge stack.child.var.* node overrides into vars
prepared = setup.Prepare(childSource, vars)
```

The daemon **seeds** the item vars into the router run's initial context
at dispatch, so the *same* values feed both the router's conditional
edges (runtime) and the child prepare (the loop above). **Context is the
single carrier end-to-end**; prepare-time vars appear only at the
child's own prepare boundary, because that is the child pipeline's
authored input contract. Pushing context *past* that boundary into the
child's nodes would require runtime attribute templating the spec
deliberately omits.

### manager_loop enhancements

`stack.manager_loop` (attractor-spec §4.11) already runs a child
sub-pipeline inline and supervises it (observe/steer/wait; returns the
child's SUCCESS/FAIL so downstream edges branch). The router needs three
small changes:

1. **Per-node child selection.** Read `stack.child_dotfile` /
   `stack.child_workdir` as **node** attrs (fall back to graph attrs),
   so multiple `manager_loop` nodes can coexist in one router graph.
   Backward-compatible — single-child graphs are unchanged.
2. **Vars from context.** Prepare the child via `setup.Prepare(childSrc,
   vars)` with `vars` pulled from context per the child's declared
   `vars=`, plus `stack.child.var.*` node overrides.
3. **Cancel the child on early return.** Today the inline child
   goroutine leaks when the loop returns on `stop_condition` /
   `max_cycles`; cancel it. (Bug fix.)

No `Runner` interface, no `HandlerEnv.Runner`, no server import — the
child runs entirely within `engine`, exactly as it does today.

### Seeded initial context

The daemon starts the router run with item vars already in context. The
mechanism reuses the existing `ctx.Apply(map)` (attractor-spec §5.1 —
already used to restore context on checkpoint resume): the engine gains
an initial-values seed applied at run start, after `MirrorGraph`. Item
vars land under their plain names (`repo`, `pr_number`, …) matching the
child pipelines' `vars=` declarations.

## Deviations from the core spec (attractor-spec.md)

For anyone building the engine to match this:

| # | Deviation | Spec today | Change | Nature |
|---|---|---|---|---|
| A | Child-selection attr **scope** | `stack.child_dotfile` / `stack.child_workdir` are graph attrs (§4.11, graph-attr table) | Also readable as **node** attrs; graph attr is the fallback | Backward-compatible extension |
| B | **Seeded initial context** + child vars from context | Fresh runs start with empty context + `MirrorGraph` (§5.1); child prepare has no var source | Run accepts a seeded initial context; `manager_loop` prepares its child with vars pulled from context | Additive; uses §11.7 context + the `vars` attr |

`manager_loop` cancelling its inline child on early return is a **bug
fix**, not a deviation.

## What this reverses (items-spec)

items-spec decisions **4, 6, 7** and the (now-deleted) `dispatch-spec.md`
described a **dispatch node** + **`Runner` seam** producing
**first-class registry-tracked child runs**. This spec supersedes them —
routing is inline sub-pipelines per §9.4. Consequences:

- No `dispatch` node type; no `Runner` interface / `HandlerEnv.Runner`.
- `item_ref` stays **out of the engine** — only the router run carries
  it (daemon-stamped, I4).
- The work run is nested, not a sibling: **one registry run per
  dispatch**. The UI surfaces the deepest executing graph as the run
  name.

The first-class-child model remains a *possible future* if a
concurrency/fan-out use case ever needs independently listable /
cancelable work runs; it is not needed for the review/implement roadmap.

## Milestone ledger

| # | Milestone | Deps | Status |
|---|---|---|---|
| R1 | `manager_loop` reads `stack.child_dotfile`/`child_workdir` as node attrs (graph fallback) | — | todo |
| R2 | Run accepts a seeded initial context (`ctx.Apply` on start); daemon seeds item vars at dispatch | — | todo |
| R3 | `manager_loop` prepares its child via `setup.Prepare` with vars from context (+ `stack.child.var.*` overrides) | R1, R2 | todo |
| R4 | `manager_loop` cancels its inline child on `stop_condition`/`max_cycles` early return | — | todo |
| R5 | `POST /items/dispatch {item_ref}` → start the router graph with seeded context + item_ref | R2 | todo |
| R6 | A `router` pipeline (conditionals → `manager_loop` targets; agent fallback → `decision`) | R1, R3, R5 | todo |
| R7 | Docs: deviations (this spec), items-spec 4/6/7 reversal, attractor-spec back-pointers | — | todo |

## Testing conventions

- **R1**: two `manager_loop` nodes in one graph with distinct node-level
  `stack.child_dotfile` each run their own child.
- **R2**: a run started with a seeded context exposes those keys to the
  first node (a conditional branches on a seeded value).
- **R3**: a `manager_loop` whose child declares `vars="x"` prepares the
  child with `x` sourced from context; `stack.child.var.x` overrides
  context.
- **R4**: a child still running when `stop_condition` fires is cancelled
  (no leaked goroutine).
- **R5/R6**: `POST /items/dispatch` for a PR item runs the router, which
  routes to the review child; the single run carries `item_ref`.

## Not in scope

- The jj-workspace isolation layer per run (items-spec decision 10,
  later).
- Re-introducing first-class child runs (only if fan-out demands it).
