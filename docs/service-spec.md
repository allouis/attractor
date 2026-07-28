# Attractor service spec (draft)

Where attractor goes next: provider-routed backends, a real daemon,
a UI, and automations. Informed by a survey of fabro's feature set
(noted per section); scoped deliberately smaller.

Status: draft for discussion. Nothing here is built.

## Motivation

Today a run picks exactly one codergen backend for the whole process
(`--backend`), the `codergen.*` type strings all route to that same
backend, and `serve` wires no backend at all (simulation only). The
graph can't say "this node uses Opus, that node uses GPT-5", and the
server can't be left running as the place where workflows live.

Target shape: a long-running `attractor serve` daemon that accepts
workflows for any repo, routes each node to the right agent via
config, streams progress to a built-in UI, and fires scheduled
automations. The CLI `run` path keeps working standalone with the
same semantics.

## 1. Provider/model routing (config layer)

Nodes declare intent, never mechanism:

```dot
plan   [llm_model="claude-opus-4-8"]                      // provider inferred
review [llm_provider="openai", llm_model="gpt-5"]
cheap  [llm_model="claude-haiku-4-5"]
```

`llm_provider` / `llm_model` are already spec vocabulary
(codergen-backends-spec §2). A config file maps provider → backend
mechanism:

```toml
# ~/.attractor/config.toml, overridden by ./.attractor/config.toml
default_provider = "anthropic"

[providers.anthropic]
backend   = "acp"                  # acp | claudecode | simulation
command   = "claude-agent-acp"
model_env = "ANTHROPIC_MODEL"      # how llm_model reaches the agent

[providers.openai]
backend   = "acp"
command   = "codex-acp"
model_env = "CODEX_MODEL"

[providers.anthropic-cli]          # legacy tier-2 path, still available
backend   = "claudecode"
```

Resolution per codergen node:

1. `llm_provider` attr, else inferred from `llm_model` prefix
   (`claude*` → anthropic, `gpt*`/`o*` → openai, `gemini*` → google),
   else `default_provider`.
2. Config entry → backend instance (constructed once per provider,
   cached; ACP backends keep their per-thread session maps).
3. `llm_model` injected via `model_env` (later: ACP
   `session/set_config`).

Precedence for the agent command stays: node `acp_command` > graph
`acp_command` > provider config. `--backend` / `--acp-cmd` become
run-wide overrides for debugging and eventually deprecate.

Lint additions: `provider_known` (node references a provider missing
from config — warning, config is machine-local), `model_env_missing`
(provider sets no `model_env` but node sets `llm_model`).

Fabro reference: model stylesheets + `[llm.providers.*]` catalog.
Attractor's spec already sketches stylesheet-based model selection
(§2.3); stylesheets can layer on later — attrs first.

## 2. Run/serve parity

One shared setup path (extract from `cli.Run` into an internal
package) used by both entry points:

- parse → transforms (`PromptFile`, `Stylesheet`) → lint →
  registry built from the provider config above.
- `serve` gains what `run` has: vars, `@file` prompts, real backends.
- Submit payload grows:

```json
POST /pipelines
{ "dot": "...", "vars": {"name": "world"}, "cwd": "/home/agent/repo-a" }
```

- `cwd` in the payload behaves as the graph-level `cwd` default
  (node/graph attrs still win). This is the multi-repo story: one
  daemon, each submitted workflow targets its repo via cwd.
- `base_dir` for `@file` prompt resolution: default to `cwd`.

Fabro reference: single `WorkflowRunEngine` parameterized by
interviewer, used by both CLI and server. Same idea.

## 3. Daemon hardening

- **Concurrency limit**: `--max-concurrent-runs` (default ~4);
  submitted runs queue FIFO (`submitted` → `running`), visible in the
  registry. Today every submit starts a goroutine immediately.
- **Run lifecycle states**: `queued | running | success | fail |
  canceled` in the registry summary (superset of today).
- **systemd unit** example in docs; the daemon is just
  `attractor serve --bind 127.0.0.1:7681`.
- CLI convergence (later, docker-model): `attractor run` detects a
  local daemon and submits + streams SSE back to the terminal;
  standalone fallback unchanged. Until then `run` stays standalone.

Explicit non-goals for now (fabro has them, we don't want them yet):
sandboxes/containers, git-branch checkpointing, child-run trees,
billing rollups, MCP servers, Slack.

### Run persistence (no DB)

The filesystem stays the source of truth: one directory per run under
the logs root (`manifest.json`, `source.dot`, `checkpoint.json`,
`events.jsonl`, stage artifacts). The server registry already reloads
it on startup. Changes needed:

- `GET /pipelines` — expose the registry list (exists internally,
  no endpoint). Summaries: id, status, started, pipeline name, cwd.
- Move `events.jsonl` writing into the shared setup path so CLI runs
  persist events too (today only server runs do), making any run
  replayable in the UI.
- If cross-run queries ever matter, add a rebuildable SQLite index
  over the manifests; the directories remain canonical. Not now.

## 4. UI

Single static page embedded in the binary, served at `/ui`:

- **Run list**: registry summaries, states, links.
- **Run view**: graph SVG (server renders DOT via existing graphviz
  path; node ids are addressable in the SVG output), colored live
  from SSE — active node pulses, completed green, failed red,
  retrying amber. Page refresh replays `events.jsonl` then attaches
  live SSE.
- **Node pane**: click node → streamed `assistant_delta` text,
  `tool_call` events, links to `prompt.md` / `response.md` /
  `tool_calls/`.
- **wait.human dock**: list open questions, answer buttons (endpoints
  exist already).

No framework, no build step: one HTML file + vanilla JS. If the UI
outgrows that, revisit; do not start with React.

Fabro reference: their web app's run board / stage detail / interview
dock — same features, 1% of the surface.

## 5. Automations

An automation = saved run config + trigger. Stored as TOML files
under `~/.attractor/automations/` (file-first, like pipelines; no DB):

```toml
# ~/.attractor/automations/nightly-triage.toml
pipeline = "~/.attractor/pipelines/triage/pipeline.dot"
cwd      = "/home/agent/repo-a"

[vars]
label = "bug"

[trigger]
cron = "0 3 * * *"     # five-field cron, local time
```

- Daemon loads/watches the directory; `POST /automations/{name}/run`
  fires one manually (UI button); cron scheduler submits through the
  same path as `POST /pipelines`.
- `attractor automations list|run <name>` CLI.
- Later: GitHub webhook trigger. Not in v1.

Fabro reference: `automations/` TOML + api/schedule triggers +
materializer. Same shape minus repos-by-origin cloning.

## 6. Later / stolen ideas parking lot

Worth stealing eventually, explicitly not now:

- **Retry presets + failure classes** (`transient` vs `deterministic`
  routable in edge conditions) — attractor has basic retries; classes
  make goal-gate loops much safer.
- **Stall watchdog** — kill a stage after N minutes without events.
  Cheap and valuable; candidate for early adoption in the ACP backend.
- **Steering** — inject user messages mid-turn over ACP; needs the
  long-lived-process backend mode.
- **Token/cost accounting** — ACP `usage_update` notifications exist;
  surface per-stage tokens in events, roll up per run.
- **Model stylesheets** — CSS-like selectors assigning models by
  class/id, layered over §1.
- **Artifact globs** — collect build outputs per stage.

## Milestones

The Status column is the execution ledger: the self-dev pipeline
(`pipelines/self-dev/`) picks the first `todo` milestone, implements
it, and flips its Status to `done` in the milestone's final commit.
Only that pipeline (or a human) edits this column.

| # | Deliverable | Depends on | Status |
|---|---|---|---|
| M1 | Provider config + per-node routing in `run` | — | done |
| M2 | Shared setup path; serve parity (vars, prompts, backends, cwd payload) | M1 | done |
| M3 | Queue + run states + concurrency limit; `GET /pipelines`; shared events.jsonl | M2 | done |
| M4 | UI v1 (run list, live graph, node pane, human dock) | M2 | done |
| M5 | Automations (TOML + cron + manual trigger) | M3 | done |
| M6 | Stall watchdog; usage events | any | done |

Each milestone is a handful of small commits with e2e tests (fake ACP
agent already covers the agent side; provider config gets its own
fixture-driven tests).
