# Attractor web UI build spec

The operational dashboard for the daemon: browse **items** (Linear
issues, GitHub PRs) and kick off runs for them, watch the fleet of
active/past **runs** (how many, what stage, who needs a human), and
browse the **workflows** that can be run. One vanilla-JS page served at
`/ui`, extended from the existing stub — no framework, no build step,
embedded in the binary.

Status: **plan for the build pipeline.** Design settled via a grilling
session (see the decision record at the end). Each milestone is
implemented in its own run and its Status flipped to `done` in the
milestone's final commit (same ledger convention as `service-spec.md`
and `items-spec.md`).

## Motivation

The daemon (`attractor serve`) is the place where real work runs:
you design/plan interactively, file the work as a Linear issue or a
GitHub PR, and hand it to the daemon. What's missing is the surface to
*drive and watch* that: pick an item and run it, and see at a glance
how many runs are live, what stage each is at, and which are waiting on
you.

The backend for this already exists. items-spec Phase 1 (I1–I5, all
`done`) shipped `GET /items` (GitHub + Linear, annotated with linked-run
state) and `POST /items/run`. The run registry, per-run SSE, the
live-colored run graph, and the wait.human answer endpoints all exist
and are exercised by the current `/ui` stub. **This spec is the
missing view layer**, and it retargets items-spec **I6** (the Items
view, previously scoped to the — now parked — TUI) onto the web.

## Architecture principle

The HTTP API is the contract; the UI is a thin client of it, same as
`service-spec §4`. Extend the existing `internal/server/ui/index.html`
(a small hash-routed SPA) into three top-level views plus detail. Stay
one embedded HTML file for v1; if it outgrows readability, split the JS
into a few statically-served files later — still no framework.

Reuses these existing endpoints unchanged: `GET /items`,
`POST /items/run`, `GET /pipelines` (the run list), `GET /pipelines/{id}`,
`GET /pipelines/{id}/events` (SSE), `GET /pipelines/{id}/graph`,
`GET /pipelines/{id}/stages/{node}`, `POST /pipelines/{id}/cancel`,
`GET /pipelines/{id}/questions`,
`POST /pipelines/{id}/questions/{qid}/answer`,
`GET /pipelines/{id}/artifacts/{path...}`.

Adds exactly two endpoints (W2):

- `GET /workflows` → `[{name, path}]` listing `~/.attractor/pipelines/*/`
  (each dir's `pipeline.dot`). Catalog root is `~/.attractor/pipelines/`
  (configurable later). Serves **both** the Workflows view and the Items
  run-picker (whose `POST /items/run` `pipeline` field wants a path).
- `GET /workflows/{name}/graph` → SVG via `render.SVG` (same renderer as
  `/pipelines/{id}/graph`, but from the definition file — no run needed).

## Terminology

Binding for this doc, the UI, and new code (recorded in items-spec's
ubiquitous-language section):

- **workflow** — the abstract graph / definition (a `.dot`). Was
  overloaded as "pipeline"; that word is retired except where upstream
  owns it (the pristine `docs/spec/attractor.md`, the legacy
  `pipeline.dot` filename, and the legacy `/pipelines` endpoint paths).
- **run** — one execution instance of a workflow. Already the dominant
  term (`RunSummary`, `~/.attractor/runs/<id>/`, `ListRuns`/`GetRun`).
- **item** — a live projection of external work (Linear issue, GitHub
  PR), identity `(source, type, external-id)`. Never stored. A run
  optionally carries an `item_ref` linking it back.

## Views

Three-tab SPA over hash routing, plus click-through detail views.
Default landing is **Items** (the entry point for kicking off work).

```
Items (default) · Runs · Workflows            [☀/☾ theme toggle]
────────────────────────────────────────────────────────────────
#items                 browse items → run one
#runs                  fleet list of runs
#workflows             browse workflow definitions
#run/<id>              run detail (graph + node pane + gate dock)
#workflow/<name>       workflow detail (static graph)
```

**Interlinking is the payoff of having all three:** a run row links to
its item (`item_ref`) and its workflow; an item row shows its linked
runs (badge → jump to the run); a workflow shows "run this via an item".

### Items view (default)

- Fetch **per source and merge client-side** (`GET /items?source=github`
  and `?source=linear`; the endpoint is single-source). Candidate set is
  "assigned to me / my open PRs" — what the sources return today.
- **Frontend filtering** (v1): narrow the merged set by source, type,
  repo (`vars.repo`), free-text on title, and in-progress state. Sort /
  group by repo or source. This narrows only; broadening the candidate
  set (unassigned PRs, arbitrary repos/states) is v2 backend `Filter`
  work, explicitly out of scope here.
- Each item shows a **linked-run / in-progress badge** (from the `GET
  /items` annotation) → click to jump to the run.
- **Run action**: pick item → pick a **workflow** (from `GET /workflows`)
  → repo (auto-filled for PRs from `vars.repo`; picked from the repo map
  for non-PR items) → `POST /items/run {item_ref, pipeline: <path>, repo}`
  → link to the resulting run.

### Runs view (the fleet)

- Run-centric list of active + past runs from `GET /pipelines`,
  **poll-refreshed ~2–3s** (same pattern as the existing gate dock).
- Answers the core question at a glance: **counts** (running / queued /
  needs-human / failed), status per run with the state palette, and a
  **prominent needs-human flag** — the highest-attention state.
- Each row links to its **item** (`item_ref`) and its **workflow**;
  a **cancel** action (`POST /pipelines/{id}/cancel`) with confirm.
- Per-row *live stage-by-stage* progress is a v2 nicety; v1 rows show
  status + counts, and stage-by-stage lives in the detail view.

### Workflows view

- List workflows from `GET /workflows` → click → **static graph** of the
  definition (`GET /workflows/{name}/graph`, reusing the detail graph
  pane's styling). Read-only.
- **Deferred:** child-graph expansion (drilling into a
  `stack.manager_loop` node's child workflow) and editing.

### Run detail (already built — polish only)

The existing detail view already delivers the progress experience:
graph SVG **live-colored via SSE** (running pulses, completed green,
failed red, retrying amber), a node pane tailing `assistant_delta` /
`tool_call` output with artifact links, and the wait.human answer dock.
v1 restyles it to the token system and wires the item/workflow
backlinks; the dynamic graph itself is done.

## Design system

A quality bar applied to every view, established **before** any view is
built (W1), so nothing is retrofitted.

- **Tokens, not ad-hoc values.** CSS custom properties for a neutral
  surface/text ramp, a spacing + typography scale, and the semantic
  state colours below. Every view consumes tokens only. Replaces the
  current stub's light-only `--bg:#fff` / GitHub-ish colours.
- **Dark + light**, via `prefers-color-scheme` auto **plus a manual
  toggle** persisted to `localStorage`. Every token has a light and dark
  value; text and state chips meet **WCAG AA** contrast in both.
- **Semantic state palette** — defined once, the load-bearing colour
  decision (this is what makes the fleet view glanceable). `needs-human`
  is deliberately the highest-attention hue. Indicative values, tuned
  per theme at build time:

  | state | role | light | dark |
  |---|---|---|---|
  | `queued` | muted/neutral | `#64748b` | `#94a3b8` |
  | `running` | active + pulse | `#2563eb` | `#60a5fa` |
  | `success` | done | `#16a34a` | `#4ade80` |
  | `failed` | error | `#dc2626` | `#f87171` |
  | `needs-human` | **attention (pops)** | `#d97706` | `#fbbf24` |
  | `retrying` | transient | `#ea580c` | `#fb923c` |
  | `cancelled` | inert | `#94a3b8` | `#64748b` |

- **Borrow fabro's *look*** (layout density, chip/badge styling, the
  run waterfall, the interview dock) as visual reference — ideas, not
  code (it's a 58k-line React app; we stay vanilla).
- **Every milestone's "done" includes:** correct in both themes, state
  colours consistent, and no unstyled/janky loading, empty, or error
  states.

## Liveness

- **Runs list**: poll `GET /pipelines` ~2–3s, re-render rows + counts.
- **Items in-progress badges**: refreshed on the same poll tick.
- **Run detail**: the existing per-run SSE (`/pipelines/{id}/events`) —
  live stage colouring + output tail. Unchanged.
- List-level SSE (instant fleet updates) is a v2 upgrade — an additive
  endpoint, no rework of the poll UI.

## Auth / remote

Inherited, zero v1 work. `serve --bind` defaults to loopback (no auth);
non-loopback requires `--auth-token` or `--insecure`; a `withAuth`
bearer gate exempts the static `/ui`. Local = loopback; remote = a
Tailscale-IP bind with `--insecure` (Tailscale is the network-layer
auth). *Edge, not v1:* under `--auth-token` the browser's `fetch`/SSE
would need to send the token — Tailscale + `--insecure` sidesteps it.

## Non-goals (v1)

- Backend `Filter` rework / broadening the item candidate set (v2).
- List-level SSE for the fleet (v2; poll is enough).
- Interactive client-side graph (pan/zoom, `@viz-js/viz`) and
  workflow child-graph expansion (later).
- Workflow editing in the UI (later).
- Routing / auto-dispatch (`router` workflow) — separate track
  (items-spec Phase 2); the run-picker can already target `router`.
- A framework or build step. If the single file outgrows readability,
  split the JS into statically-served files — still vanilla.

## Conventions for the build pipeline

- Extend `internal/server/ui/index.html`; new endpoints in
  `internal/server` (workflows catalog) reusing `internal/render`.
- Server handlers get tests against `httptest` (canned dirs / registry);
  match existing server-test style. The static page is exercised via the
  existing e2e UI tests (`tests/e2e_ui*.go`) where practical.
- Gate: `nix develop -c go test ./... -race` + `nix build .#attractor` +
  gofmt clean. Small atomic jj commits; match existing style.

## Milestones

The Status column is the execution ledger: the self-dev pipeline picks
the first `todo`, implements it, and flips its Status to `done` in the
milestone's final commit. Only that pipeline (or a human) edits it.

| # | Deliverable | Depends on | Status |
|---|---|---|---|
| W1 | Design foundation + nav shell: token system (surface/text ramp, spacing/type scale, state palette), dark/light with `prefers-color-scheme` + persisted toggle; restyle the existing Runs list + Run detail to tokens; 3-tab nav (Items/Runs/Workflows) + hash routing, default Items | — | done |
| W2 | `GET /workflows` catalog + `GET /workflows/{name}/graph` (SVG via `render.SVG`), server-side, with tests | — | done |
| W3 | Items view: per-source fetch + client merge, frontend filters (source/type/repo/text/in-progress), sort/group, linked-run + in-progress badges | W1, W2 | done |
| W4 | Items run action: workflow-picker (from catalog) + repo resolution → `POST /items/run`, link to the resulting run | W3 | done |
| W5 | Runs view: run-centric list, poll-live counts (running/queued/needs-human/failed), prominent needs-human flag, item↔run + workflow backlinks, cancel with confirm | W1 | todo |
| W6 | Workflows view: catalog list + static graph render; wire the item/workflow backlinks across all views | W2, W5 | todo |

Run detail (dynamic graph, node pane, gate dock) is already built; W1
restyles it and W6 adds its backlinks — no rebuild.

---

## Decision record

Settled in the grilling session, most-load-bearing first:

1. **Target = daemon runs, dispatched from items.** The daemon is the
   execution substrate for real work; the self-dev standalone loop was a
   stopgap. The UI's job: kick runs off, and show how many are live,
   what stage, who needs a human.
2. **Web, not TUI.** Vanilla JS extending the existing `/ui`. The TUI
   (`tui` bookmark, T4–T7 built but stale) is **parked** — choosing web
   retires the rebase-vs-redo question. A graph — wanted, and free on the
   web (server renders DOT→SVG) — was the deciding factor.
3. **Borrow fabro's UX ideas, not its stack.** `fabro-web` is a ~58k-line
   React SPA; adopting it would reverse the spec's no-framework decision
   and add a build toolchain. Take layout/styling/UX, stay vanilla.
4. **workflow (definition) + run (instance);** retire "pipeline". Chosen
   over "workflow + pipeline" because "run" is already the instance term
   everywhere and "pipeline=graph" is upstream-owned.
5. **This is items-spec I6, retargeted TUI→web.** The whole intake
   backend (I1–I5) is `done`; only the view was missing.
6. **Frontend filtering for v1.** Narrows the assigned set; backend
   `Filter` rework is v2.
7. **Poll the fleet list; SSE in detail.** Poll matches the existing
   dock pattern and needs no backend; per-run SSE (live stage colouring)
   already exists.
8. **Dynamic progress graph is already built** (SSE-colored run-detail
   graph). Workflows view = static. Interactive/client-side graph +
   child-expansion deferred.
9. **Beauty is a foundation, not a finish.** Tokens + dark/light +
   semantic state palette land in W1 and gate every view.
</content>
</invoke>
