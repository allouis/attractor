# Core / items code separation — spec

Pull the **work-intake (items) layer** out of the core engine packages
into `internal/items/`, so the DOT-pipeline engine (attractor-spec) and
our items→pipeline addon (items-spec) are legibly separate. No behaviour
change — pure reorganisation + one small type change (`item_ref` becomes
an opaque tag so the engine/registry carry zero items knowledge).

Status: **ready to build** (milestone ledger below).

## What is "items" (not core)

Core = the DOT engine per `docs/attractor-spec.md`: `dot`, `graph`,
`engine`, `handler`, `backend`, `acp`, `transform`, `condition`, `render`,
`lint`, `interviewer`, `artifact`, `setup`, `automation`, `cron`,
`scheduler`, the `server` daemon, and the CLI.

Items = our addon per `docs/items-spec.md`, currently scattered:
- `internal/engine/itemref.go` — `ItemRef` type (**parked in engine, which never uses it**)
- `internal/config/repos.go` — repo→path map
- `internal/source/*` — GitHub/Linear sources
- `internal/server/items.go` — `GET /items`, `POST /items/run`

## Target layout

```
internal/items/
  itemref.go            ← from internal/engine/itemref.go   (items.ItemRef)
  repos.go              ← from internal/config/repos.go      (items.Repos)
  source/{types,github,linear}.go  ← from internal/source/*
  httpapi/items.go      ← from internal/server/items.go      (Register(mux, deps))

internal/{engine,config,server,...}  ← core, items-free
```

`internal/config` keeps only provider config; `internal/toml` stays
shared infra (both configs use it — legitimate, no split).

## Milestone ledger

| # | Milestone | Deps | Status |
|---|---|---|---|
| CS1 | Create `internal/items`; move `ItemRef` there (`engine.ItemRef`→`items.ItemRef`); update all refs in `source/*`, `server/*`. Engine loses the unused type. | — | done |
| CS2 | Move `config/repos.go` → `internal/items/repos.go` (`config.Repos`→`items.Repos`); update `cli`, `server`. `config` becomes pure provider config. | CS1 | done |
| CS3 | Move `internal/source/*` → `internal/items/source/*`; update importers (`server`, `cli`). Package name `source` unchanged. | CS1 | done |
| CS4 | Make the Run's item link an **opaque string tag** (below). Engine + `server/registry` carry zero `ItemRef` knowledge; the items layer builds/parses the string. | CS1 | done |
| CS5 | Extract items HTTP into `internal/items/httpapi` with `Register(mux, deps)` for `GET /items` + `POST /items/run`; `server.New` mounts it. Move the items tests. `server.go` no longer names item handlers. | CS2, CS3, CS4 | done |
| CS6 | Docs — this spec's status; a short "code layout" note in `items-spec.md`; confirm `internal/{engine,config,server}` are `ItemRef`-free. | CS5 | done |

## CS4 — the opaque `item_ref` tag

Today `Run.itemRef` is a typed `*ItemRef`, forcing `internal/server` to
import the items type. Change:

- `Run` stores `itemRef string` — an **opaque tag** (e.g. `"github:pr:42"`),
  persisted in the run manifest as-is.
- `NewRun(..., itemRef string)`; `RunsForItem(ref string)` groups runs by
  **string equality** — no parsing, no `ItemRef`.
- `Run.Summary()` exposes the string unchanged.
- The **items HTTP layer** owns the mapping: it builds the tag from
  `items.ItemRef` (a canonical `source:type:id` string) when submitting,
  and parses it back when it needs the typed form. The `submit` path
  takes the tag as a plain `string`.
- `internal/client/types.go` already mirrors the shape as its own type —
  update its doc comment to "mirrors items.ItemRef"; no import.

Result: `internal/engine` and `internal/server` have **no dependency on
the items package** for the run's item link. The typed `items.ItemRef`
lives only in `internal/items`, used by sources and the items HTTP layer.

## Testing conventions

- **CS1–CS3**: pure moves — the full suite (`go test ./... -race`) stays
  green; `grep -rn "engine.ItemRef\|config.Repos\|internal/source" internal/`
  finds only the new paths.
- **CS4**: a run submitted with tag `"github:pr:42"` is found by
  `RunsForItem("github:pr:42")`; two runs for the same item group
  together; `internal/server` no longer imports `internal/items` for the
  Run field (grep confirms).
- **CS5**: `GET /items` + `POST /items/run` behave identically (existing
  items e2e tests pass) with the handlers now in `internal/items/httpapi`.

## Not in scope

- Moving item→vars→repo→pipeline *composition* to the TUI/client (the
  "engine-pure, TUI owns items" topology bet — deferred by decision).
- Moving `pipelines/{review,implement,router}` (cosmetic; breaks test
  paths for little gain).
- Any behaviour change to sources, routing, or the HTTP surface.
