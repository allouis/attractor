# AGENTS.md — operating attractor on a fresh box

This file makes the repo self-sufficient: a clone can build attractor, run
pipelines, and work the self-dev loop that builds attractor with attractor.
It's the **command sheet** — for the mental model (run-dir layout, engine
loop, `status.json` contract), read the README's "How it works" first.

attractor is a DOT-pipeline AI-workflow runner (Go). The unit is a
**self-contained run**: `attractor run --ui` executes one pipeline and
serves its own live API + waterfall. `attractor hub` is the optional
pull-based directory. There is no daemon — anything that orchestrates runs
(VMs, cron, CI) lives outside and just invokes `attractor run`.

## Prerequisites

- **nix** (flakes) builds the binary; the dev shell provides Go, gofmt, jj,
  graphviz. There is no global Go — every `go` goes through `nix develop -c`.
- **Auth** — `claude-agent-acp` reads `~/.claude/.credentials.json` (from a
  `claude` login) or `ANTHROPIC_API_KEY`.

## Build and test

```bash
nix build .#attractor              # wrapped binary (ACP adapters, jj, graphviz on PATH)
./result/bin/attractor help
nix develop -c go test ./...       # full suite, offline-safe
nix develop -c go test ./tests/ -run TestName -count=1   # one test
```

Use the wrapped `./result/bin/attractor` for agent runs (not a bare
`go build`) so the ACP adapters resolve on PATH.

## Conventions

- **TDD.** For a bug, add a failing test that reproduces it *first*; for a
  feature, write the tests for the expected behavior. The suite is the gate.
- All Go through `nix develop -c` (no global toolchain).
- Small, atomic, reviewable commits.

## Running pipelines

The task is one freeform `brief` (issue text, a spec milestone, or a plain
paragraph); `base` is the target branch a draft PR lands on.

**The one gotcha — backend selection:** with **no** `--backend`, each node
routes through the built-in providers (real agents) and per-node models come
from `--stylesheet`. Passing `--backend acp|claude|simulation` is a run-wide
override that ignores routing (every node on one model). Full flag / var /
event reference: [docs/running-pipelines.md](docs/running-pipelines.md).
Dispatch command sheet (per-pipeline var contracts, gates, remote viewing):
[.claude/skills/dispatching-pipelines/SKILL.md](.claude/skills/dispatching-pipelines/SKILL.md)
(a Claude Code agent picks it up automatically).

Dogfood attractor on itself — hand `plan-build-review` a brief, pointed at
this repo. It plans, human-gates, implements, runs the checks + five-lens
review, and opens a draft PR:

```bash
./result/bin/attractor run --ui --cwd $PWD --stylesheet pipelines/models.css \
  --logs ~/.attractor/runs/<change> \
  -var brief="Implement <milestone> from docs/<spec>.md: …" -var base=main \
  -var 'check.deps=nix develop -c true' \
  -var 'check.typecheck=nix build .#attractor' \
  -var 'check.lint=out=$(nix develop -c gofmt -l .); [ -z "$out" ] || { echo "gofmt drift:"; echo "$out"; exit 1; }' \
  -var 'check.test=nix develop -c go test ./...' \
  pipelines/plan-build-review/pipeline.dot
```

These four checks decompose this repo's CI (`nix flake check` = gofmt-drift +
`nix build .#attractor` + `go test ./...`) — match CI exactly or the pipeline
ships a change CI then rejects. `check.*` commands must fail non-zero AND
PRINT their diagnostics: `gofmt -l .` alone exits 0 on drift (gate never
fails), and a silent `test -z "$(gofmt -l .)"` fails but prints nothing
(fix agent blind) — the wrapper above does both. Full dispatch command sheet:
[.claude/skills/dispatching-pipelines/SKILL.md](.claude/skills/dispatching-pipelines/SKILL.md).
Debug any run via `<logs>/events.jsonl` and the span dirs
`<node>@v<visit>.a<attempt>/`; `--ui` shows the same live.

## The hub (optional)

```bash
attractor hub --bind 127.0.0.1:7690 --dir ~/.attractor/hub
attractor run --announce http://127.0.0.1:7690 … pipeline.dot   # self-registers
```

The hub pulls each live run's own API and stores its completion archive as
the permanent record — pull-only, so a hub outage never loses data. Remote
runs: pass `--ui-token <t>` (it rides the announce).

## Repo layout

- `internal/engine` — traversal, retries, loop guards, event log
- `internal/handler` — codergen / tool / wait.human / parallel + contract checks
- `internal/backend` — error classification; `acp`, `claudecode`, `router` (provider routing), `fake`
- `internal/transform` — subgraph inlining, @file prompts, stylesheet
- `internal/lint` — validation incl. `context_refs`
- `internal/runserver` + `internal/runview` — the single-run API/UI + event-log folds
- `internal/hub` — announce/scrape/archive/launch
- `tests/` — e2e suite over public APIs
