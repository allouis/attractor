# Running pipelines — a runbook

How to dispatch an attractor pipeline and drive it to completion, whether
by hand or from an agent (e.g. Claude Code). For the DOT authoring model
see the spec; for model routing see the [Models](#models) section below.

## Mental model

One run is one self-contained directory. `attractor run` walks the
pipeline graph node by node: **tool** nodes execute a shell command,
**codergen** nodes hand a prompt to an agent that does real file I/O in the
working tree and self-reports via `{stage_dir}/status.json`, **wait.human**
nodes block for a human answer, **subgraph** nodes are inlined at load
time. Edges route on the resolved outcome (`condition="outcome=fail"` sends
a failure to a fix/responder node). Loop guards abort a futile run. When
the terminal node is reached the run ends `success` or `failed`.

## Quick start

```bash
nix build .#attractor          # wrapped binary: PATH carries claude-agent-acp, codex-acp, jj, graphviz
./result/bin/attractor run plan-build-review \
  --cwd /path/to/repo \
  --stylesheet pipelines/models.css \
  -var brief="Add X: …the change to make…" -var base=main \
  -var "check.deps=…" -var "check.typecheck=…" \
  -var "check.lint=…" -var "check.test=…"
```

Always use the **wrapped** `./result/bin/attractor` (or `nix run`) so the
ACP adapters, `jj`, and `graphviz` resolve on PATH.

## The run command

```
attractor run <name-or-path> [flags]
```

- **Name resolution.** A bare `plan-build-review` resolves to
  `./pipelines/plan-build-review/pipeline.dot`, then
  `./pipelines/plan-build-review.dot`, then `~/.attractor/pipelines/…`. A
  path containing `/` or `.dot` is used
  verbatim.
- **Backend rule (the #1 gotcha).** With **no** `--backend`/`--acp-cmd`,
  each codergen node is routed per its model through
  `~/.attractor/config.json` (real agents). Passing `--backend acp|claude|
  simulation` (or `--acp-cmd`) is a run-wide override that bypasses the
  config. So: real run → pass no `--backend`; forced backend → pass one.
  `--backend simulation` (the default when the flag is set) echoes
  `[simulated] <node>` markers and does no real work.

### Flags

| Flag | Purpose |
|---|---|
| `-var name=value` | Seed a pipeline variable (repeatable). |
| `--stylesheet <file>` | External model stylesheet (repeatable, cascading). See Models. |
| `--cwd <dir>` | Working tree the pipeline operates in (graph-level cwd default). |
| `--backend claude\|acp\|simulation` | Run-wide backend override (bypasses config). |
| `--acp-cmd <cmd>` | ACP agent command for `--backend acp`. |
| `--human auto\|console\|approve` | How `wait.human` gates are answered. |
| `--json` | Emit one JSON event per line on stdout (machine-drivable). |
| `--ui` / `--ui-addr` | Serve the run's own read-only API + waterfall UI. See UI reachability. |
| `--ui-token <tok>` | Require `Authorization: Bearer <tok>` on the loopback/public binds; mandatory for a public/LAN `--ui-addr`. |
| `--announce <hub>` | Register the run with a hub and ship its archive on completion. |
| `--logs <dir>` | Where run artefacts land (default under `~/.attractor/runs/<id>`). |

### UI reachability

With no explicit `--ui-addr`, `--ui` binds an ephemeral **loopback** port
and, when this host has a **Tailscale** interface (an interface address in
`100.64.0.0/10`), additionally binds that tailnet IP. Both URLs print to
stderr. The tailnet bind is added only for `--ui`; `--announce` alone opens
no extra port.

- **No token (`--ui` alone):** loopback and tailnet are both served
  without a bearer — a tailnet is already a private, authenticated
  network, and a browser cannot send a bearer header.
- **`--ui-token <tok>`:** the loopback bind (and any public/LAN bind)
  enforces the token; the tailnet bind stays token-free. `--announce`
  ships the token to the hub as before.

The tokenless surface still guards its mutable endpoint against
browser-borne attacks packet-auth cannot stop: a request whose `Host` is
not an IP/localhost (DNS rebinding) or that carries a cross-origin `Origin`
(CSRF) is refused. Use the printed IP URL, not a hostname.

`--ui-addr <addr>` forces a single bind and suppresses the tailnet bind.
Trust is decided from the address actually bound: if it is public or
LAN-reachable (including `0.0.0.0` / `[::]`, or a `100.64/10` address that
is **not** one of this host's detected tailnet interfaces), it **requires
`--ui-token`** and errors at startup otherwise — previously such a bind
started an open server.

> Detection is range-based: **any** `100.64.0.0/10` interface yields an
> open, tokenless tailnet bind. RFC 6598 is not Tailscale-exclusive
> (carrier NAT, other overlay VPNs, some CNIs), so on such a network the
> UI is reachable, unauthenticated, by that network's peers.

## Passing vars

`-var name=value`, repeatable. Referenced as `$context.name` and
interpolated at runtime; an undefined `$context.*` reference **fails the
node**. Check commands are seeded as `-var check.<name>=…` and MUST print
their diagnostics to stdout/stderr — the fix agent is handed that output.

### Per-pipeline var contracts

| Pipeline | Required `-var` | Notes |
|---|---|---|
| `checks` | `repo` + `check.{deps,typecheck,lint,test}` | No agent, no gates; a failing check ends the run. Thin wrapper over `checks-core`. |
| `checks-core` | `check.{deps,typecheck,lint,test}` | Shared subgraph: the deps→typecheck→lint→test chain, embedded by the others. |
| `review-core` | `diff_cmd` | Shared subgraph: the five-lens review (uses the built-in `anthropic` + `codex` providers). |
| `review-pr` | `repo,pr_number,title` | Diff-based via `gh pr diff`; no checkout. |
| `revise-pr` | `repo,pr_number,bookmark,workspace_revision` | `workspace_revision` must be the PR bookmark. Baseline checks + review + fix loop + ship→push. |
| `plan-build-review` | `brief,base` + `check.*` | plan (human gate) → implement → checks → review → ship → draft PR. `brief` is the freeform task; `base` = target branch. |

## Models

Pipelines carry role **classes**, not models; models come from an external
stylesheet:

```bash
attractor run plan-build-review --stylesheet pipelines/models.css …
```

Selectors cascade `* < shape < .class < #id`; a node's explicit
`llm_model` still wins over the sheet (for a node that must use a specific model).
Provider is inferred from the model prefix. Role classes: `.plan`,
`.build`, `.review`, `.publish` (plus finer `.coder/.fixer/.responder/
.revise/.lens/.synth`). Copy `pipelines/models.css` and edit it to mix and
match — e.g. planner on Fable, implementer on Opus.

**Fail-loud:** in a run that intends real agents (a `--stylesheet` was
passed, or `default_provider` is set in the config), a codergen node left
with no model fails the run instead of silently simulating. A bare dev run
with neither stays lenient and may simulate.

**How a model resolves to an agent.** A node's provider is its
`llm_provider`, else inferred from the model prefix (`claude*`→anthropic),
else `default_provider`. The provider maps to an agent command and the env
var the model is injected through. Two providers are **built in** —
`anthropic` (claude-agent-acp) and `codex` (codex-acp) — so the shipped
pipelines need no config. Other prefixes (`gpt*`/`o*`→openai,
`gemini*`→google) infer a provider that is *not* built in: set
`llm_provider` explicitly (the review correctness lens pins `codex` for its
GPT model) or add the provider to `~/.attractor/config.json` (an optional,
never-auto-written JSON file; format in the README). `--backend` /
`--acp-cmd` override routing entirely (every node on one backend).
`attractor validate` warns on a node whose provider isn't configured
(`provider_known`) or whose provider has no `model_env` (`model_env_missing`).

## Watching a run

`--json` streams one `engine.Event` per line. Drive a headless run by
parsing `kind`:

| kind | meaning |
|---|---|
| `pipeline_started` / `pipeline_completed` / `pipeline_failed` | run lifecycle (terminal = the latter two). |
| `stage_started` / `stage_completed` / `stage_failed` / `stage_retrying` | per-node lifecycle; `status` on completion. |
| `stage_progress` | live agent deltas, tool calls, permission grants, contract corrections (see `detail.kind`). |
| `interview_started` | a `wait.human` gate is open — carries `question` + `options`. |
| `interview_answered` / `interview_timeout` | gate resolved. |
| `checkpoint_saved` | resumable checkpoint written. |
| `usage` | token accounting. |

Without `--json`, human-readable lines print to stdout and the final line
is `pipeline <status> logs=<dir>`. `--ui` serves a live waterfall + a
`/answer` endpoint for gates.

## Logs layout

Under `--logs` (or `~/.attractor/runs/<run-id>`): `run.json`,
`events.jsonl`, `checkpoint.json`, `graph.dot`, and one span dir per node
attempt named `<node>@v<visit>.a<attempt>/` holding `prompt.md`,
`response.md`, `status.json` (engine-resolved outcome),
`agent-status.json` (the agent's own report), and `tool_calls/`.

## Revisiting finished runs

The `--ui` server is in-process — it dies when `attractor run` exits. Run
directories are self-contained, so two commands bring a finished run back:

- `attractor runs [--root <dir>]` — list local runs from the runs root
  (`$XDG_DATA_HOME/attractor/runs` or `~/.attractor/runs`), one line each:
  run id, graph name, status (`running` / `success` / `failed`, or
  `unknown` for a half-written dir), and start time. Most-recent first.
- `attractor view <dir>` — re-serve a run directory read-only over the
  **same** loopback + tailnet binding as `run --ui` (same UI, same API,
  same `--ui-addr` / `--ui-token` rules and reachability — see [UI
  reachability](#ui-reachability)). It reads the dir instead of driving an
  engine, so gates can't be answered (the `/answer` endpoint returns 409).
  URLs print to stderr; it blocks until interrupted.

Typical flow: `attractor runs` to find the run id, then `attractor view
~/.attractor/runs/<run-id>` to browse it.

## Human gates

`wait.human` nodes (plan-build-review's `plan_gate` + `ship`, revise-pr's
ship gate) block until answered:

- `--human console` — prompt on the TTY (choice + optional note).
- `--human approve` — auto-approve the first option (unattended runs).
- `--human auto` (default) — console if stdin is a TTY, else auto-approve.
- With `--ui`/`--announce`, answer over the run's `/answer` endpoint.

An unattended dispatch of a gated pipeline needs `--human approve` or a
`--ui` answer channel, or it will block.

## Gotchas

- **Auth.** `claude-agent-acp` reads `~/.claude/.credentials.json` (from a
  `claude` login) or `ANTHROPIC_API_KEY`; `codex-acp` needs its own auth.
  Auth/config errors fail the run immediately (not retried).
- **External side-effects are real.** `plan-build-review`'s `open_pr` opens a draft
  PR; `revise-pr` pushes the branch. These fire when the ship gate passes —
  shipping a run performs a GitHub write.
- **Review pipelines** run the correctness lens on the built-in `codex`
  provider; just pass a `--stylesheet` covering `.review`.
- **VM / isolation.** The pipeline runs wherever `attractor run` runs;
  `--cwd` sets the working tree. `review-pr` uses `gh pr diff` (no `.git`
  needed) on purpose for VM workspaces.
- **Config inheritance.** ACP agents run as full Claude Code with the
  operator's `HOME`, so they load the target repo's `CLAUDE.md`/`AGENTS.md`
  and the operator's global `~/.claude` — pin a clean config dir for
  reproducible runs.

## Dispatching from Claude Code

An agent can drive a run end to end: build the wrapped binary, invoke
`attractor run … --json --human approve --logs <dir>`, parse the event
stream (terminal on `pipeline_completed`/`pipeline_failed`; surface
`interview_started` if not using `--human approve`; watch `stage_failed`),
and read the failing span dir's `status.json` + `response.md` for detail.
For pipelines with external side-effects, confirm intent before shipping.
