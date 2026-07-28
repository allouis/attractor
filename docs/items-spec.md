# Items & Dispatch — design (living draft)

Adds the *work-intake* half to attractor. Today attractor executes
workflows (runs, the daemon, the TUI). This layer sits on top: pull
work from external systems, let a human (or later, automation) pick a
piece, and dispatch it to a workflow.

Status: **AGREED — ready to build.** Design settled via a grilling
session. Terminology note: **"dispatch" means the *routing* step**
("given an item, decide which workflow") — that is the *second* phase.
The MVP does no routing: you pick the item, the workflow, and the repo
yourself ("run *this* with *that*").

## Shape

```
Source ──fetch──▶ Item ──dispatch──▶ Run  (router graph may run
(GitHub,          (live            (one     a work pipeline inline
 Linear)          projection)       run)    as a sub-pipeline)
```

You browse Items pulled live from a Source (PRs assigned to you,
Linear issues), pick one, and it's dispatched to a workflow — a PR to
`review`, an issue to `implement`/`bug-fix`. A router run may run the
chosen work pipeline **inline** as a sub-pipeline (see
`docs/router-spec.md`); it is not a separate run.

## Ubiquitous language

- **Source** — a system Items are pulled from (GitHub, Linear; later
  local markdown). Responsible for *fetching* and *filtering* items.
- **Item** — a live projection of external work. Identity is
  `item_ref = (source, type, external-id)`, e.g. `(github, pr, 42)`.
  Attractor **never stores Items**; they're fetched on demand.
- **item_ref** — the durable identity linking a Run back to the Item
  that spawned it. Stored on the Run.
- **Run** — one execution of a workflow (existing concept). Optionally
  carries an `item_ref`. **One Item → 0..N Runs** over time; each Run
  → at most one Item.
- **Dispatch** — starting a Run for a given (pipeline, vars,
  item_ref). The single admission point; a human pick, a static rule,
  and a router workflow all funnel through it.
- **Runner** — *(superseded — see `docs/router-spec.md`)* originally a
  seam a dispatch node called to start a first-class child Run. The
  Phase-2 redesign runs work pipelines **inline** as sub-pipelines, so
  no such seam exists.
- **Router** (later) — a workflow that reads an Item and *emits a
  routing decision* (which workflow, or "needs design"). Not a stored
  status — a dispatch-time decision.
- **In-progress** — **derived, not stored**: an Item is being worked
  on iff it has a linked Run that is queued/running.

## Decisions

### LOCKED

> **Phase-2 redesign note.** Decisions **4, 6, 7** below are
> **superseded** by `docs/router-spec.md`: routing runs the chosen work
> pipeline **inline** as a sub-pipeline (attractor-spec §9.4 /
> `stack.manager_loop`), not via a `dispatch` node + `Runner` seam. The
> intake spine (1, 2, 3, 5, 8–11) stands. Each superseded decision is
> annotated inline.

1. **Items are live projections, never stored.** No item store, no
   sync/dedup problem. Identity `(source, type, external-id)`.
2. **Item↔Run link lives on the Run** (`item_ref`). One Item → 0..N
   Runs. In-progress / history are *derived* from the linked runs.
   The vars a run was dispatched with are its frozen item snapshot, so
   displaying "run for PR #42 — Fix login" is free.
3. **No attractor-owned Item status in v1.** "Ready" / "needs design"
   are never fields attractor stores. Source-side signals (labels,
   Linear states) drive *filtering*; triage is a *dispatch-time
   decision*, not stored state.
4. **Routing = a workflow (router graph).** ⚠ *Superseded in shape by
   `docs/router-spec.md`.* The router is itself a workflow: static
   conditional nodes (is it a PR? → review) with an agent-node fallback
   for the ambiguous cases. **Original terminal action:** a dispatch
   node. **Now:** conditional edges select among static
   `stack.manager_loop` nodes (one per target pipeline), each running
   its child **inline**. "Needs design" is a routing outcome (surface to
   human), never a stored status.
5. **Execution is daemon-only.** `attractor serve` (the registry +
   queue + scheduler) is the one execution substrate. `attractor run`
   is a **thin client that requires a running daemon** (submits +
   streams via `internal/client`); no daemon → clean error. No
   ephemeral in-process core, no run/serve unification needed.
6. ⚠ **Superseded — no `Runner` seam** (`docs/router-spec.md`). The
   original design added an `engine`-defined `Runner` interface,
   implemented by the registry, injected into `HandlerEnv`, so a
   dispatch node could start a first-class child run. The inline model
   needs none of this: `stack.manager_loop` runs the child within
   `engine`, no server import, no seam.
7. ⚠ **Superseded — no dispatch node** (`docs/router-spec.md`). The
   original node was **fire-and-forget** and produced a first-class
   child run via `Runner.Submit`. **Now:** the router uses
   `stack.manager_loop`, which runs its child **inline** and supervises
   it (so a router *can* branch on the child's failure). Prompts and
   `tool_command` interpolate `$context.*` from the live context at
   runtime (attractor-spec §4.5); `manager_loop` seeds the child's
   initial context from the parent snapshot, so the child resolves those
   values itself (see router-spec §"Child pipeline gets its vars from
   context"). `item_ref` stays on the router run only; the engine never
   touches it.

8. **Sources are pull/on-demand in v1** (fetch when the list opens or
   refreshes; webhooks are the later automated-trigger layer). A
   **Source** is `List(filter) → []Item` + `Get(item_ref) → Item`,
   mapping its API objects to the generic Item. Fetch tooling: GitHub
   via the `gh` CLI (machine-authed), Linear via an API key in config
   (the daemon can't borrow the session's MCP). **First source:
   GitHub PRs → `review`** (tightest loop; code already exists).
9. **The dispatched workflow owns its own checkout**, not attractor —
   the Item supplies vars (`repo`, `pr_number`, `branch`, `url`,
   `title`, `body`, …), the daemon sets `cwd`, and the pipeline's
   first node runs `gh pr checkout`. Requires one new config: a
   **repo→local-path map** (`github.com/allouis/attractor →
   /home/agent/attractor`, or a base-dir convention).
10. **Isolation via jj workspaces (staged), never fresh clones.**
    - MVP: `cwd` = the mapped base jj repo; **serial per repo**.
    - Workspaces layer: the daemon does `jj workspace add` off the
      mapped base per run → isolated `cwd` + **auto-snapshot history of
      the agent's work** → `jj workspace forget` + cleanup on run end.
      Same map, layered on. Base repos must be jj-colocated (they are).

11. **The daemon owns sources; TUI/CLI are clients.**
    - `GET /items?source=…&filter=…` → items fetched server-side,
      **annotated with linked-run state** (the registry knows which
      active runs carry which `item_ref`, so each item is marked
      in-progress / "has run #N").
    - **MVP** `POST /items/run {item_ref, pipeline, repo}` → the
      *human* supplies the workflow and repo; the daemon resolves the
      item → vars, repo → `cwd`, stamps `item_ref`, starts a run. **No
      routing.**
    - **v2 routing** reuses the *same* `POST /items/run` — the caller
      supplies `router` as the pipeline and the router workflow picks the
      real workflow automatically (see `docs/router-spec.md`). *No
      separate `/items/dispatch` endpoint* — routing is a pipeline
      choice, not a new admission point.
    - The TUI adds an **Items view** (toggle from Runs): source picker,
      "assigned to me" filter, in-progress badge, **pick → run** (choose
      a pipeline, or `router` to auto-route). Runs appear in the Runs
      view; item↔runs linkable. CLI clients (`attractor items
      list|run`) fall out via `internal/client`. Visual design of the
      view is build-time, not spec'd here.

## Phase 1 — MVP: run an item with a chosen workflow

**No routing, no sub-pipeline dispatch.** You pick the item,
the workflow, and the repo; the daemon just starts the run. This
validates the whole spine (item ↔ run link, item data → workflow, repo
selection) with the least machinery.

Milestone ledger (consumed by the generic build pipeline; one per run,
Status flipped to `done` in the milestone's final commit):

| # | Milestone | Deps | Status |
|---|---|---|---|
| I1 | `item_ref` `(source,type,external-id)` on runs — set at creation, exposed in the run summary / API | — | done |
| I2 | Sources (server-side): `GET /items?source=…&filter=assigned` — GitHub via `gh`, Linear via config API key; annotate each item with linked-run state | I1 | done |
| I3 | repo→path config (`~/.attractor/repos.toml`): `owner/name → local jj-colocated checkout` | — | done |
| I4 | `POST /items/run {item_ref, pipeline, repo}` — resolve item → vars, repo → `cwd`, stamp `item_ref`, start a run; PR auto-fills repo, non-PR picks from the map | I1, I2, I3 | done |
| I5 | a `review` pipeline (first node `gh pr checkout`, then a codergen review stage) | — | done |
| I6 | TUI Items view — list items, in-progress badge, **pick item → pick workflow → pick repo → run** | I4, tui branch | blocked |

I1–I5 are the backend spine, buildable on this `items` branch and
validatable by `curl`. **I6 is `blocked`** — it needs the TUI, whose
fate (rebase vs redo) is undecided; skip it until then.

## Phase 2 — Workflow dispatch (routing)

Now attractor picks the workflow *for* you. **Full design:
`docs/router-spec.md`.** In short:

- **The router is just a pipeline.** Static conditionals (is it a PR? →
  review) + agent fallback → a routing decision; "needs design" as an
  outcome surfaced to the human. Conditional edges select among static
  `stack.manager_loop` nodes (one per target pipeline).
- **Run it through the existing `POST /items/run`** — no new endpoint.
  You pick `router` as the pipeline instead of picking `review`
  directly; the router makes the work-pipeline choice. A client that
  wants "just route it" defaults the pipeline field to `router`.
- **`stack.manager_loop` enhancements** — child selection as a node
  attr, child initial context seeded from the parent context (child reads
  `$context.*` at runtime). The chosen work
  pipeline runs **inline** as a sub-pipeline (attractor-spec §9.4) — no
  dispatch node, no `Runner` seam, no first-class child run.
- **Submit seeds context** — `/items/run` seeds the Item's vars +
  `item.type`/`item.source` into the run's initial context so the
  router's conditional edges can branch at runtime.

## Later (designed, not scheduled)

- **jj-workspace isolation layer** (concurrent runs per repo).
- **More sources / item types**; richer filters.
- **`attractor run` as daemon client** (execution unification).
- **Webhook / cron auto-dispatch** (automation on the same spine).

## Architecture notes

- `attractor run` reuses `internal/client` (built during the TUI
  work) — it's the same submit+stream a TUI/API client does.
- Backend selection is the daemon's (provider-config router), not a
  per-invocation flag, since runs go through the daemon.
- Routing reuses attractor's existing composition primitive — the
  **sub-pipeline node** (attractor-spec §9.4), i.e. `stack.manager_loop`
  running a child **inline**. The work is nested inside the router run
  (one registry run per dispatch), visible via `stack.child.*`
  telemetry; the UI surfaces the deepest executing graph as the run
  name. See `docs/router-spec.md`.

## Code layout

The items layer is quarantined in `internal/items/` so the DOT engine
and this addon stay legibly separate (see `docs/code-split-spec.md`):

```
internal/items/
  itemref.go            items.ItemRef — canonical source:type:id tag
  repos.go              items.Repos — repo→path map
  source/{types,github,linear}.go   Item sources
  httpapi/items.go      Register(mux, deps) — GET /items + POST /items/run
```

Invariant: `internal/engine`, `internal/config`, and the run registry
carry **zero `ItemRef` knowledge**. A run's item link is an **opaque tag
string** (`"github:pr:42"`), grouped by string equality; the typed
`items.ItemRef` lives only in `internal/items`, used by sources and the
items HTTP layer. `internal/server` names the type in exactly one place
— `server.go`'s `POST /pipelines` decode, which accepts an `item_ref`
object and stringifies it to a tag before admission — so the engine and
registry never see the type.
