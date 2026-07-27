# Dispatch node — spec

A new node type, `dispatch`, that starts a **first-class child run**
from inside a workflow. It's the composition primitive attractor
lacks: `stack.manager_loop`'s child is inline/invisible, and there's no
graph `import`. A `dispatch` node submits a run to the registry — the
child shows up in the run list, is cancelable, and is linked to the
same Item as its parent.

This is **Phase 2** of `docs/items-spec.md` (the router). It builds on
the locked decisions there — §6 (Runner seam), §7 (dispatch node
contract) — and on the I1 `item_ref` work. The **router** (a workflow
that reads an Item and decides which pipeline, emitting the dispatch)
is the *consumer* of this node and is out of scope here; this spec is
just the node + the seam it needs.

Status: **ready to build** (milestone ledger below).

## Design

### The `Runner` seam

The engine can't import the server (`server` imports `engine` → cycle),
so the seam is an **interface defined in `engine`**, implemented by the
daemon's registry, and threaded into every handler:

```go
// package engine
type Runner interface {
    // Submit starts a child run and returns its id (fire-and-forget).
    Submit(RunRequest) (runID string, err error)
}

type RunRequest struct {
    Pipeline string            // path or bare name, resolved by the Runner
    Vars     map[string]string
    Cwd      string
    ItemRef  *ItemRef          // usually inherited from the parent run
}
```

- `HandlerEnv` gains a `Runner Runner` field; `engine.Config` gains a
  `Runner` the engine copies into each `HandlerEnv`.
- The daemon's `Run.execute()` (registry) passes **itself** (a registry
  adapter) as `engine.Config.Runner`. The registry's `Submit` resolves
  the pipeline → prepares it → `NewRun(..., itemRef)` → `go execute`,
  returning the id.
- **Standalone `attractor run` has no Runner** (nil) — a `dispatch`
  node there fails with a clear "dispatch requires the daemon" error.
  That matches items-spec §5 (execution is daemon-only); until
  `attractor run` becomes a daemon client, dispatch is a daemon-only
  feature, which is fine (the router is a daemon workflow).

### The `dispatch` node

Behaves like any node (items-spec §7): **fire-and-forget**, returns
`SUCCESS`, hands off via context/conditions (not prompt interpolation).

- **Target pipeline**: node attr `dispatch.pipeline`, else the context
  value `dispatch.pipeline` (so a router's agent node can set it).
- **Vars**: `dispatch.var.<name>="..."` node attrs, merged over any
  the router placed in context.
- **cwd**: inherits the parent run's `cwd` (same repo) unless
  `dispatch.cwd` overrides.
- **item_ref**: auto-inherited from the parent run (same Item).
- **Output**: `SUCCESS` with `ContextUpdates = {dispatched.run_id,
  dispatched.pipeline}` so downstream **edge conditions** can branch
  (e.g. `dispatched.run_id != ""`). Waiting/supervising a child stays
  `stack.manager_loop`'s job.
- Registered as type `dispatch` in both `cli.buildRegistry` and
  `server.DefaultHandlers`.

## Milestone ledger

| # | Milestone | Deps | Status |
|---|---|---|---|
| D1 | `Runner` interface + `RunRequest` in `engine`; `HandlerEnv.Runner` + `engine.Config.Runner`, copied into each env | — | todo |
| D2 | Registry implements `Runner.Submit` (resolve pipeline → prepare → `NewRun` → execute); `Run.execute` injects the registry as `Config.Runner` | D1 | todo |
| D3 | `dispatch` node handler (target/vars/cwd/item_ref resolution, fire-and-forget, `ContextUpdates`, nil-Runner error); register the type | D1, D2 | todo |
| D4 | Lint rule (`dispatch` node needs `dispatch.pipeline` attr or context source) + docs (README + this spec) | D3 | todo |

## Testing conventions

- **D1**: a fake `Runner` set on `engine.Config`; assert the engine
  copies it into a handler's `HandlerEnv.Runner` (drive one node).
- **D2**: server test — a graph with a node whose handler calls
  `env.Runner.Submit` produces a **second registry run**, visible via
  `List`, carrying the inherited `item_ref`.
- **D3**: unit-test the `dispatch` handler with a fake `Runner`:
  assert the `RunRequest` (pipeline from attr and from context; vars
  merged; cwd inherited; item_ref inherited) and the `ContextUpdates`;
  a nil Runner returns a FAIL outcome naming the daemon requirement.
- **D4**: lint rule test (a `dispatch` node with no target → warning).

## Not in scope (downstream)

- The **router pipeline** (static conditionals → agent fallback →
  `dispatch` node) — a *workflow* built on this node; belongs in
  `items-spec` Phase 2 once the node exists.
- `POST /items/dispatch` (the routed counterpart of `/items/run`).
- `attractor run` as a daemon client (would let standalone runs
  dispatch too).
