# Web UI v2 spec — the self-service test

Make the web UI good enough that a **team member with no server access**
— no SSH, no `~/.attractor/runs/` — can do the whole job from the browser:
find work, launch it, watch it, **debug a failure**, unblock it, and
control it. Plus a visual pass so it's something you'd hand to a
colleague. Extends `docs/web-ui-spec.md` (the 3-tab SPA already shipped);
same vanilla-JS, no build step.

Status: **design proposal.** Framed as a gap analysis against the shipped
UI; milestone ledger is the execution contract.

## The test

> Give a teammate the tailnet URL and nothing else. A run they started
> fails. Can they figure out *why* and either fix the input or unblock it
> — without asking someone with shell access?

Today: **no.** The shipped UI (`internal/server/ui/index.html`, 3 tabs:
Items / Runs / Workflows) can list items, launch an item-run, show live
run status with a colored graph, and cancel. But to understand a run you
still SSH in and read `events.jsonl` + the stage dirs. This spec closes
that gap and makes the surface presentable.

## Gap analysis (shipped → needed)

| Job to be done | Shipped | Gap |
|---|---|---|
| Find work | Items tab, filters | ok |
| Launch a run | basic item-run picker (repo datalist) | the full run modal (workflow dropdown, declared vars, repo dropdown, standalone launch) — `run-workflow-spec` |
| See the fleet | Runs tab: counts, cancel, poll | ok; add filter/search + provenance |
| Watch a run live | run detail + SSE graph coloring | no live **node output tail**, no tokens, no current-stage focus |
| **Debug a failure** | — | **the core gap:** per-stage prompt / response / tool-calls, the produced **diff**, artifacts, failure reason, full event log — all endpoints exist server-side, the UI calls none of them |
| Unblock a gate | — | `GET …/questions` + answer endpoints exist, **UI never calls them** — a needs-human run is a dead end in the browser |
| Control a run | cancel only | **retry / re-run**, and re-run-from-failure |
| Configure the daemon | — | the Config tab — `config-screen-spec` |
| Look good | tokens, dark/light | a real visual pass: hierarchy, spacing, components, empty/loading/error states, responsive |

The striking part: the **debug** and **gate** endpoints already exist
(`GET /pipelines/{id}/stages/{node}` T2, `/questions`, `/artifacts/…`,
`/events`) — the UI simply doesn't use them. Much of v2 is *wiring the
browser to APIs the daemon already serves*, not new backend.

## Themes & decisions (proposed)

- **Debuggability is the priority.** The run-detail view becomes a real
  investigation surface: the graph (already live) **plus** a stage
  inspector (click a node → its prompt, response, tool-calls, status,
  timing), a **diff** view of what the run produced, an artifacts browser,
  the failure reason surfaced at the top, and a full scrollable event
  timeline with live tail. This is what replaces SSH.
- **Everything live.** Node output tails via SSE (`assistant_delta`); the
  fleet, counts, and needs-human flags refresh without reload. A blocked
  run is visually loud (it needs a human).
- **Operable from the browser.** Answer gates inline (option buttons →
  answer endpoint), cancel, and **re-run** (resubmit the same workflow +
  vars + repo). No action requires a shell.
- **Presentable.** A small design system (already have tokens + dark/light)
  taken to a consistent component set — cards, tables, badges, buttons,
  modals — with real empty/loading/error states and a sensible responsive
  layout. Pretty enough to hand to the team.
- **Access model (flagged, not resolved here).** UI-only team access over
  the tailnet inherits the config-screen posture: tailnet = trust, no app
  roles. That means a teammate can also edit config/secrets. If that's not
  acceptable, we need a read-only-vs-operator split — **out of scope for
  v2**, but called out because "give my team access" raises it.

## Milestones

| # | Deliverable | Depends on | Status |
|---|---|---|---|
| U1 | **Visual pass**: design-system cleanup (components: cards/tables/badges/buttons/modals), layout + spacing, empty/loading/error states, responsive; no behaviour change | — | done |
| U2 | **Stage inspector**: click a node → panel with prompt / response / tool-calls / status / timing via `GET …/stages/{node}` (T2); live tail of `assistant_delta` | — | done |
| U3 | **Event timeline**: full scrollable, filterable run event log (via `…/events` SSE + replay cursor), failure reason surfaced prominently at the run header | — | done |
| U4 | **Diff + artifacts**: show the run's produced change (diff) and browse `…/artifacts/{path}` from the run detail | — | todo |
| U5 | **Gate answering**: render open questions (`…/questions`) inline in run detail; option buttons POST the answer; run resumes; needs-human made loud in the fleet | — | todo |
| U6 | **Run control**: re-run (resubmit workflow+vars+repo) and re-run-from-failure; cancel already shipped | run-workflow | todo |
| U7 | **Fleet polish**: run filter/search + provenance (item / workflow / repo / started-by) columns and deep-linkable run URLs | U1 | todo |

## Non-goals (v2)

- Editing workflow `.dot` from the UI.
- Role-based access / per-user auth (flagged above, separate effort).
- Graph editing (view + expand only, already shipped).
- Notifications (gate-open Slack/webhook is a server-side concern).
- A build step / framework — stays vanilla JS.

## Relationship to the other specs

- **`run-workflow-spec`** supplies the launch modal (U6 re-run reuses
  `POST /workflows/{name}/run`).
- **`config-screen-spec`** supplies the Config tab (a fourth nav entry
  U1's shell should leave room for).
- **`repo-vm-config-spec`** surfaces per-repo runner/image inside that
  Config tab. Together the four specs are the "a teammate with only the
  browser can work" bundle.
