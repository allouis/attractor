# AGENTS.md — operating attractor on a fresh box

This file makes the repo self-sufficient: an agent (or human) with just a
clone can build attractor, run pipelines, and work the self-dev loop that
builds attractor with attractor.

attractor is a DOT-pipeline AI-workflow runner (Go). The unit of the
system is a **self-contained run**: `attractor run --ui` executes a
pipeline and serves its own live API + waterfall view; `attractor hub` is
the optional pull-based directory (announce + scrape + archive + launch).
There is no daemon: anything that orchestrates runs (VMs, cron, CI, work
intake) lives OUTSIDE this tool and just invokes `attractor run`.

Read the README's "How it works" section first — it is the mental
model (run dir layout, engine loop, loop guards, agent status.json
contract) everything here builds on.

## 1. Prerequisites / toolchain

- **nix** (flakes) — builds the binary; the dev shell provides Go, gofmt,
  jj, graphviz. There is no global Go: every `go` invocation goes through
  `nix develop -c`.
- **Claude auth** — the bundled `claude-agent-acp` reads
  `~/.claude/.credentials.json` (from `claude` login) or
  `ANTHROPIC_API_KEY`.

## 2. Build and smoke

```bash
nix build .#attractor          # wrapped binary: PATH carries claude-agent-acp, codex-acp, jj, graphviz
./result/bin/attractor help
nix develop -c go test ./...   # full suite, offline-safe
```

The wrapped binary matters for agent runs: use `./result/bin/attractor`
(not a bare `go build`) so the ACP adapters resolve on PATH.

## 3. Running pipelines

```bash
# Simulation (no LLM), good for wiring checks:
attractor run --backend simulation -var name=world path/to/pipeline.dot

# Real agent + live waterfall UI:
attractor run --backend acp --acp-cmd claude-agent-acp --ui \
  --cwd /path/to/target-repo --logs ~/.attractor/runs/my-run \
  -var brief="…the change to make…" -var base=main \
  -var "check.deps=..." -var "check.typecheck=..." \
  -var "check.lint=..." -var "check.test=..." \
  pipelines/plan-build-review/pipeline.dot
```

Notes:
- The task is one freeform `brief` (issue text, a spec milestone, or a
  plain paragraph); `base` is the target branch the PR lands on.
- `--backend acp` runs every codergen node on the adapter's default
  model. For per-node model control, drop `--backend` and pass
  `--stylesheet pipelines/models.css`; the `anthropic` + `codex` providers
  are built in, so no config file is needed (see docs/running-pipelines.md).
- `check.*` seeds are the repo's deterministic check commands the
  plan-build-review/revise-pr tool nodes run. Make them PRINT their diagnostics
  (a silent `test -z "$(gofmt -l .)"` leaves the fix agent blind).
- `--human approve` auto-approves gates for unattended runs; `--ui`
  answers gates from the browser; a TTY gets a console prompt.
- Bare pipeline names resolve under `./pipelines/` then
  `~/.attractor/pipelines/`.

## 4. The hub (optional)

```bash
attractor hub --bind 127.0.0.1:7690 --dir ~/.attractor/hub
# runs announce themselves:
attractor run --announce http://127.0.0.1:7690 ... pipeline.dot
# or let the hub spawn them:
curl -X POST 127.0.0.1:7690/pipelines \
  -d '{"path":"plan-build-review","cwd":"/work/repo","vars":{"brief":"…","base":"main"}}'
```

The hub scrapes each live run's own API (`/pipelines/{id}`,
`/events?since=`), proxies gate answers, and stores the completion
archive as the permanent record. Scrape failure = unreachable flag, not
data loss. Remote runs: give the run `--ui-token <t>`; the token rides
the announce and the hub authenticates with it.

## 5. Self-dev loop (dogfooding)

Dogfood attractor on itself with the `plan-build-review` pipeline: hand it a
`brief` describing the change (a spec milestone works well), pointed at
this repo. It plans, human-gates, implements in small jj commits, runs
the checks + five-lens review, and opens a draft PR for you to merge.

```bash
./result/bin/attractor run --backend acp --acp-cmd claude-agent-acp --ui \
  --cwd $PWD --logs ~/.attractor/runs/<change> \
  -var brief="Implement <milestone> from docs/<spec>.md: …" -var base=main \
  -var "check.deps=nix develop -c true" \
  -var "check.typecheck=nix develop -c go vet ./..." \
  -var "check.lint=nix develop -c gofmt -l ." \
  -var "check.test=nix develop -c go test ./... -race -count=1" \
  pipelines/plan-build-review/pipeline.dot
```

To debug any run: read `<logs>/events.jsonl` and the per-attempt stage
dirs (`<node>@v<visit>.a<attempt>/prompt.md`, `response.md`,
`status.json`). The waterfall (`--ui`) shows the same data live.

## 6. Repo layout

- `internal/engine` — traversal, retries, loop guards, event log
- `internal/handler` — codergen / tool / wait.human / parallel + contract checks
- `internal/backend` — error classification; `acp`, `claudecode`, `router` (provider routing), `fake`
- `internal/transform` — subgraph inlining, @file prompts, stylesheet
- `internal/lint` — validation incl. `context_refs`
- `internal/runserver` + `internal/runview` — the single-run API/UI and the event-log folds
- `internal/hub` — announce/scrape/archive/launch
- `tests/` — e2e suite over public APIs; TDD is the norm (bugs get a failing test first)
