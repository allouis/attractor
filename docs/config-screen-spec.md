# Config-screen spec

One daemon-owned config for everything the daemon needs — registered
repos and their check commands, the Linear key, provider routing — edited
from a **Config** view in the web UI instead of hand-editing scattered
files. Makes the per-repo `[checks]` from `run-workflow`/the workflow
redesign actually configurable without touching each repo.

Status: **draft for the grilling session.** Milestone ledger is the
execution contract once agreed. Open decisions are flagged inline for the
grill.

## Motivation

Config is scattered across three places today: `~/.attractor/config.toml`
(providers, Linear key), `~/.attractor/repos.toml` (repo→path map), and —
as the check backbone landed — a `[checks]` table in **each repo's own**
`.attractor/config.toml`. That per-repo choice is portable but the wrong
fit for a UI: the daemon would have to read/write files inside repos it
doesn't own. For a daemon with a UI, config should be **central** (one
place the daemon owns) and **editable in the UI**. This also lets the
run-workflow repo dropdown and the check commands come from one source.

## Decisions (proposed — for the grill)

- **Central config, daemon-owned.** One config the daemon reads and
  writes, holding: registered repos (`owner/name → path`), per-repo check
  commands (deps/typecheck/lint/test), the Linear key, and provider
  routing (default provider + `[providers.*]`).
- **Checks move central.** The per-repo `.attractor/config.toml [checks]`
  is replaced by per-repo check commands in the central config. The
  daemon's `seedChecks` reads them from there instead of `config.Load`
  over the repo cwd.
- **A Config view** in the web UI (a fourth nav tab): edit repos (+ their
  checks), the Linear key, and providers.
- **Storage = JSON the daemon owns** *(open — see below)*. The UI edits
  via an API; storing config as JSON the daemon serialises avoids teaching
  the deliberately-tiny TOML parser nested per-repo tables and makes the
  round-trip trivial.

## Data model

```jsonc
{
  "default_provider": "anthropic",
  "providers": { "anthropic": { "backend": "acp", "command": "claude-agent-acp", "model_env": "ANTHROPIC_MODEL" } },
  "linear": { "api_key": "lin_…" },
  "repos": {
    "TryGhost/Ghost": {
      "path": "/home/agent/Ghost",
      "checks": { "deps": "pnpm install --frozen-lockfile", "typecheck": "…", "lint": "…", "test": "…" }
    }
  }
}
```

The repo map + checks + Linear key + providers become one document.
`items.Repos` (the `owner/name → path` map) and `config.Config` are
projections of it.

## Endpoints

- **`GET /config`** → the config document, **secrets redacted** (Linear
  key shown as `set`/`unset`, never echoed) *(open: redaction policy)*.
- **`PUT /config`** → replace the document *(open: whole-doc vs granular
  `PUT /config/repos/{name}`, `PUT /config/linear`, …)*. Validates before
  persisting (paths exist, provider backends known).
- `GET /repos` (from run-workflow-spec R2) becomes a projection of
  `config.repos`.

## The Config view (UI)

A fourth nav tab, sectioned:

- **Repos** — a table of `owner/name → path` with the four check commands
  per row; add / edit / remove. This is the surface the check backbone
  needs and the run-workflow repo dropdown reads.
- **Linear** — the API key as a secret input (write-only; shows `set`,
  never the value).
- **Providers** — default provider + each provider's backend/command/
  model_env *(open: providers editable in v1, or read-only display?)*.

Save → `PUT /config`; inline validation errors; a saved/dirty indicator.

## Migration

On first load the daemon reads the existing `config.toml` + `repos.toml`
(+ any per-repo `[checks]`) into the central document, then writes it out
once; the old files can be retired. *(open: keep reading the TOML files as
an import path, or hard-cut?)*

## Security

The Config view reads/edits **secrets** (Linear key; later, tokens) and is
reachable over the tailnet. Even with redaction on read, `PUT` accepts
secret values. This raises the bar on the daemon's auth story: a
tailnet-only bind is the current guard, but a write-config surface may
warrant the bearer-token path being on by default for non-loopback binds.
*(open: require `--auth-token` when the Config write API is enabled?)*

## Non-goals (v1)

- Editing workflow definitions / pipeline files from the UI.
- Multi-user config / per-user overrides.
- Live provider credential testing.
- Secret storage beyond the config file (no vault integration).

## Milestones

| # | Deliverable | Depends on | Status |
|---|---|---|---|
| C1 | Central config document (schema + load/save), migrating config.toml + repos.toml + per-repo [checks] into it; `items.Repos`/`config` become projections; tests | — | todo |
| C2 | `GET /config` (secrets redacted) + `PUT /config` (validated); `GET /repos` reprojected; tests | C1 | todo |
| C3 | `seedChecks` reads per-repo checks from the central config, not the repo cwd; test | C1 | todo |
| C4 | Config view: repos table (+ checks) CRUD, Linear key secret input, providers; save via `PUT /config` | C2 | todo |
| C5 | Auth/polish: config-write auth story, validation surfaces, empty/loading/error states | C4 | todo |

## Open decisions (for the grill)

1. **Storage format** — JSON (daemon-owned, easy round-trip) vs extend the
   TOML parser for nested per-repo tables.
2. **PUT granularity** — whole document vs per-section endpoints.
3. **Secret handling** — redaction on read is clear; do we also gate the
   write API behind auth by default?
4. **Providers in v1** — editable, or read-only display (routing is
   fiddly and rarely changed)?
5. **Migration** — one-time import then retire the TOML files, or keep the
   files as a supported input alongside the UI?
