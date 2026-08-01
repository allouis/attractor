# Config-screen spec

One daemon-owned config for everything the daemon needs — registered
repos and their check commands, the Linear key, provider routing — edited
from a **Config** view in the web UI instead of hand-editing scattered
files. Makes the per-repo `[checks]` from the workflow redesign actually
configurable without touching each repo, and gives every *future* config
knob a single home.

Status: **design settled** via a grilling session (decision record below).
The milestone ledger is the execution contract; the self-dev pipeline
flips each Status to `done` in its final commit.

## Motivation

Config is scattered across three places today: `~/.attractor/config.toml`
(providers, Linear key), `~/.attractor/repos.toml` (repo→path map), and —
as the check backbone landed — a `[checks]` table in **each repo's own**
`.attractor/config.toml` (`seedChecks` reads it via `config.Load` over the
run's cwd, `server.go:375`). That per-repo choice is portable but the
wrong fit for a UI: the daemon would have to read/write files inside repos
it doesn't own. For a daemon with a UI, config should be **central** (one
place the daemon owns) and **editable in the UI**. This also lets the
run-workflow repo dropdown (`GET /repos`) and the check commands come from
one source.

## Decision record (locked)

| Topic | Decision |
|---|---|
| **Ownership / storage** | **Daemon-owned JSON** — one `~/.attractor/config.json` the daemon reads *and* writes. The UI is the editor. Replaces the two TOML files and the per-repo `[checks]`. |
| **Scope** | **Everything** — registered repos + per-repo checks, the Linear key, and provider routing are all editable in the Config view. This is the *canonical, extensible* config surface: all future config routes through here. |
| **Auth / secrets** | **Tailnet-trust posture, unchanged.** No new app-auth. The existing `withAuth` (bypass on loopback / `/healthz`, bearer token for non-loopback binds) already covers it. Secrets are **redacted on read** — never echoed. |
| **Writes** | **Whole-doc `PUT /config`** (one tab, no concurrency concern) with **secret-merge**: an empty/absent secret field keeps the stored value; a non-empty one replaces it. Everything else is replace-wholesale. |
| **Migration** | **None.** Hard cut. A fresh `config.json` with a sensible default on first run; the old TOML files are abandoned (no import code). Re-enter the handful of settings in the UI once. |
| **Effect timing** | **repos + checks are live** on the next run (read per-dispatch). **Providers + Linear source** are startup-built, so editing them shows a *"restart to apply"* note. Fully-live provider/source reload is a follow-up. |
| **Validation** | **Reject structural, warn soft.** `PUT` fails (nothing saved) on an unknown provider `backend` or a `default_provider` that names no provider. A repo `path` that doesn't resolve saves with a ⚠ warning. Secrets and shell commands are unvalidated. |
| **Extensibility** | **Hand-built panels.** One Config view, one bespoke panel per config type, over the whole-doc JSON. A future config type = a new panel + a field in the doc — not a schema DSL or a new endpoint. |

## Data model

`~/.attractor/config.json`, owned and rewritten by the daemon:

```jsonc
{
  "default_provider": "anthropic",
  "providers": {
    "anthropic": { "backend": "acp", "command": "claude-agent-acp", "model_env": "ANTHROPIC_MODEL" }
  },
  "linear": { "api_key": "lin_…" },        // redacted on read; secret-merge on write
  "repos": {
    "TryGhost/Ghost": {
      "path": "/home/agent/Ghost",
      "checks": { "deps": "pnpm install --frozen-lockfile", "typecheck": "…", "lint": "…", "test": "…" }
    }
  }
}
```

- `config.Config` (`DefaultProvider`, `Providers`, `LinearAPIKey`) and
  `items.Repos` (`owner/name → path`) become **projections** of this one
  document.
- **Checks move from flat to per-repo.** Today `config.Checks` is a flat
  `name→command` map because `Load` runs over a single repo's cwd. In the
  central doc checks are nested **under each repo**, and stored as an open
  `name→command` map (not four fixed fields) so a future pipeline
  referencing `check.e2e` needs only a config entry, not a schema change.
  The Config view surfaces the four standard checks (deps / typecheck /
  lint / test) the current pipelines reference.

## Shared config: CLI *and* daemon read it

`config.Load` has two callers — the daemon's `seedChecks`
(`server.go:382`) and the CLI's provider resolution (`cli.go:561`, the
`attractor run` path). So the storage change is not daemon-only: **both**
entry points must read `config.json`. The daemon additionally *writes* it
(via the UI); the CLI only reads. Providers + Linear key are the shared
surface; repos + checks are daemon-only (dispatch-time). One document,
two readers, one writer.

## Endpoints

- **`GET /config`** → the whole document, **secrets redacted**: `linear`
  is returned as `{ "api_key_set": true|false }`, never the value.
- **`PUT /config`** → replace the whole document. **Secret-merge:** a
  `linear.api_key` that is empty/absent keeps the stored key; a non-empty
  one sets it. An explicit clear (rare) is a distinct action. **Validation
  before persist:** reject on an unknown provider `backend`
  (∉ `acp|claudecode|simulation`) or a dangling `default_provider`; save
  with a warning on a repo `path` that doesn't resolve.
- **`GET /repos`** (run-workflow-spec R2) becomes a projection of
  `config.repos` — the run-form repo dropdown reads it.

Both `/config` routes sit behind the existing `withAuth` — i.e. open on
loopback / over `tailscale serve`, bearer-gated only on a non-loopback
bind. No config-specific auth is added (decision: tailnet is the trust
boundary).

## The Config view (UI)

A fourth nav tab, hand-built panels:

- **Repos** — a table of `owner/name → path` with the four check commands
  per row; add / edit / remove. The surface the check backbone needs and
  the run-workflow repo dropdown reads.
- **Linear** — the API key as a secret input: shows `set` / `unset` (never
  the value); empty-on-save keeps the stored key; a **Clear** button
  removes it.
- **Providers** — default provider + each provider's backend / command /
  model_env. Editing these shows a **"restart the daemon to apply"** note
  (startup-built).

Save → whole-doc `PUT /config`; inline validation errors (structural
rejections) and soft warnings (unresolved paths); a saved/dirty indicator.

## Security

The Config view edits **secrets** (the Linear key; later, tokens) and sets
**executable commands** (check commands and a provider's `command`, both
run by the daemon). The trust model is the **tailnet**: with
`tailscale serve → 127.0.0.1` the daemon sees loopback and `withAuth` is
bypassed, so any tailnet device can already reach every endpoint and
dispatch runs (which execute agent commands). Editing the commands those
runs use is therefore not a *new* capability. The one hard rule is
**redact secrets on read**. If the tailnet ever stops being fully trusted,
revisit by gating the config write API behind the bearer token even on
loopback.

## Non-goals (v1)

- Editing workflow definitions / pipeline files from the UI.
- Multi-user config / per-user overrides.
- Live provider credential testing.
- Fully-live provider/source reload (edit-then-restart for those in v1).
- Secret storage beyond the config file (no vault integration).

## Milestones

| # | Deliverable | Depends on | Status |
|---|---|---|---|
| C1 | `config.json` schema + load/save (JSON), replacing the TOML `Load`/`LoadRepos`; `config.Config`/`items.Repos` become projections; per-repo nested checks; fresh-default on first run (no migration); update both readers (`seedChecks`, CLI provider path); tests | — | todo |
| C2 | `GET /config` (secrets redacted) + `PUT /config` (whole-doc, secret-merge, structural validation / soft warnings); `GET /repos` reprojected off `config.repos`; tests | C1 | todo |
| C3 | `seedChecks` reads per-repo checks from the central config keyed by the run's repo, not the repo cwd; test | C1 | todo |
| C4 | Config view: hand-built panels — repos + checks table CRUD, Linear secret input (+ Clear), providers form with restart-note; whole-doc save via `PUT /config` | C2 | todo |
| C5 | Polish: validation surfaces (reject vs warn), redaction UX, empty/loading/error/dirty states | C4 | todo |
