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
Source ──fetch──▶ Item ──dispatch──▶ Run ──(dispatch node)──▶ Run …
(GitHub,          (live            (first-class,
 Linear)          projection)       registry-tracked)
```

You browse Items pulled live from a Source (PRs assigned to you,
Linear issues), pick one, and it's dispatched to a workflow — a PR to
`review`, an issue to `implement`/`bug-fix`. A run may itself dispatch
further runs (a router run → a work run).

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
- **Runner** — the seam a dispatch node calls to start a Run:
  `Submit(pipeline, vars, item_ref) → run handle`. Implemented by the
  daemon's registry.
- **Router** (later) — a workflow that reads an Item and *emits a
  routing decision* (which workflow, or "needs design"). Not a stored
  status — a dispatch-time decision.
- **In-progress** — **derived, not stored**: an Item is being worked
  on iff it has a linked Run that is queued/running.

## Decisions

### LOCKED

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
4. **Routing = a dispatch node inside the workflow model.** The router
   is itself a workflow: static conditional nodes (is it a PR? →
   review) with an agent-node fallback for the ambiguous cases; its
   terminal action is a **dispatch node**. "Needs design" is a routing
   outcome (surface to human), never a stored status.
5. **Execution is daemon-only.** `attractor serve` (the registry +
   queue + scheduler) is the one execution substrate. `attractor run`
   is a **thin client that requires a running daemon** (submits +
   streams via `internal/client`); no daemon → clean error. No
   ephemeral in-process core, no run/serve unification needed.
6. **The dispatch node works via a `Runner` seam** — an interface
   defined in `engine` (no import cycle), implemented by the registry,
   injected into `HandlerEnv` by the daemon. Because every run
   executes inside the daemon, a dispatch node always has the registry
   in-process — no standalone/served asymmetry.
7. **Dispatch node contract**: it behaves like any node —
   **fire-and-forget** (calls `Runner.Submit`, does not wait), returns
   SUCCESS with `ContextUpdates = {dispatched.run_id,
   dispatched.pipeline, …}` so downstream **edge conditions** can
   branch on it (prompts can't read runtime context — only prepare-time
   `-var`s — so the handoff is via conditions/context, not prompt
   interpolation). Reads its target pipeline from a node attr
   (`dispatch.pipeline`) or a context var (so a router agent can set
   it); auto-inherits `item_ref` from the parent run; passes vars from
   attrs/context. Waiting/supervising a child stays `stack.manager_loop`'s
   job. (A `tool` node shelling `attractor run --detach` is a valid
   *prototype* but not the shipped path — shell-quoting item data +
   subprocess-self-HTTP is friction the in-process node avoids.)

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
    - **v2** `POST /items/dispatch {item_ref}` → *routes* (static map,
      then router workflow) to pick the workflow automatically.
    - The TUI adds an **Items view** (toggle from Runs): source picker,
      "assigned to me" filter, in-progress badge, **pick → dispatch**.
      Dispatched runs appear in the Runs view; item↔runs linkable. CLI
      clients (`attractor items list|dispatch`) fall out via
      `internal/client`. Visual design of the view is build-time, not
      spec'd here.

## Phase 1 — MVP: run an item with a chosen workflow

**No routing, no dispatch node, no `Runner` seam.** You pick the item,
the workflow, and the repo; the daemon just starts the run. This
validates the whole spine (item ↔ run link, item data → workflow, repo
selection) with the least machinery.

Milestone ledger (consumed by the generic build pipeline; one per run,
Status flipped to `done` in the milestone's final commit):

| # | Milestone | Deps | Status |
|---|---|---|---|
| I1 | `item_ref` `(source,type,external-id)` on runs — set at creation, exposed in the run summary / API | — | done |
| I2 | Sources (server-side): `GET /items?source=…&filter=assigned` — GitHub via `gh`, Linear via config API key; annotate each item with linked-run state | I1 | todo |
| I3 | repo→path config (`~/.attractor/repos.toml`): `owner/name → local jj-colocated checkout` | — | todo |
| I4 | `POST /items/run {item_ref, pipeline, repo}` — resolve item → vars, repo → `cwd`, stamp `item_ref`, start a run; PR auto-fills repo, non-PR picks from the map | I1, I2, I3 | todo |
| I5 | a `review` pipeline (first node `gh pr checkout`, then a codergen review stage) | — | todo |
| I6 | TUI Items view — list items, in-progress badge, **pick item → pick workflow → pick repo → run** | I4, tui branch | blocked |

I1–I5 are the backend spine, buildable on this `items` branch and
validatable by `curl`. **I6 is `blocked`** — it needs the TUI, whose
fate (rebase vs redo) is undecided; skip it until then.

## Phase 2 — Workflow dispatch (routing)

Now attractor picks the workflow *for* you:

- **Dispatch node + `Runner` seam** — workflow-driven run creation.
- **Router workflow** — static conditionals (is it a PR? → review) +
  agent fallback → a routing decision; "needs design" as an outcome
  surfaced to the human.
- **`POST /items/dispatch {item_ref}`** — the routed counterpart of
  `/items/run`.

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
- The dispatch node is the composition primitive attractor lacked
  (no graph import; `stack.manager_loop`'s child is inline/invisible).
  It produces first-class, registry-tracked child runs.
