---
name: dispatching-pipelines
description: >-
  Command sheet for dispatching attractor pipelines with `attractor run` —
  the run command shape and name resolution, per-pipeline `-var` contracts
  (plan-build-review, amend-pr, revise-pr, review-pr, checks), the rule that
  `check.*` must match CI exactly, human gates (`--human`, the plannotator
  plan-review bridge), and remote viewing (`--ui` tailnet bind, `attractor
  runs`, `attractor view`). Use when asked to run/dispatch a pipeline, plan-
  build-review a change, amend or revise a PR, run the checks, or watch a run
  from another machine.
---

# Dispatching attractor pipelines

Command sheet. For the mental model (run-dir layout, engine loop,
`status.json`), read [docs/running-pipelines.md](../../../docs/running-pipelines.md).

## Run command shape

```
attractor run <pipeline> [--cwd <target-repo>] [--stylesheet models.css] \
  [--ui] [--human approve|console] [--logs <dir>] -var name=value ...
```

- `<pipeline>` — bare name or a `.dot` path. Bare names resolve in order:
  `./pipelines/<name>/pipeline.dot` → `~/.attractor/pipelines/<name>/…` →
  `$ATTRACTOR_PIPELINES/<name>/…` (the shipped bundle; the nix-wrapped binary
  sets `ATTRACTOR_PIPELINES`). So **runs work from any directory**, not just
  the attractor repo. cwd and `~/.attractor` still win over the bundle.
- `--stylesheet models.css` falls back to the bundle too (a leading
  `pipelines/` is stripped). Assigns per-node models by role class.
- `--cwd <target-repo>` — the working tree the pipeline operates in.
- Use the wrapped `./result/bin/attractor` (from `nix build .#attractor`) so
  the ACP adapters, `jj`, and `graphviz` resolve on PATH.

**Backend gotcha:** pass **no** `--backend` for real per-node-model runs —
routing then comes from the stylesheet. `--backend acp|claude|simulation` is
a run-wide override that bypasses stylesheet routing (every node on one
model). Don't pass it when you want the stylesheet to pick models.

Vars are `$context.<name>` at runtime; an undefined `$context.*` **fails the
node**, so a dispatch missing a declared var is rejected up front.

## Per-pipeline var contracts

Read from each `pipeline.dot`'s `vars=` line plus checks-core's
`$context.check.*` references. `check.*` = `check.{deps,typecheck,lint,test}`.

| Pipeline | `-var` contract |
|---|---|
| `plan-build-review` | `brief`, `base`, `check.{deps,typecheck,lint,test}` |
| `amend-pr` | `repo`, `pr_number`, `bookmark`, `workspace_revision`, `brief`, `check.*` |
| `revise-pr` | `repo`, `pr_number`, `bookmark`, `workspace_revision`, `check.*` |
| `review-pr` | `repo`, `pr_number`, `title` |
| `checks` | `repo`, `check.*` |

- **plan-build-review** — `brief` is the freeform task (issue text, spec
  milestone, or a paragraph); `base` is the target branch a draft PR lands
  on. plan (human gate) → implement → checks → five-lens review → ship gate
  → draft PR.
- **amend-pr** — full plan→build→review cycle on an EXISTING PR, pushed back
  to the same PR. The workspace **must be materialized at the PR branch**
  (seed `workspace_revision` = the PR bookmark, or `--cwd` a `gh pr checkout`
  of the branch), else it silently amends the host's `@`.
- **revise-pr** — review the PR branch, fix findings, push back in place.
  Baseline checks + review + fix loop both run checks-core, so `check.*` is
  needed even though it isn't in `vars=`. Same `workspace_revision` rule.
- **review-pr** — diff-based via `gh pr diff` (no checkout, works on a VM
  with no `.git`). No `check.*`.
- **checks** — no agents, no gates; a failing check fails the run. Thin
  wrapper over checks-core.

## THE RULE THAT BITES — `check.*` MUST match CI exactly

The `check.*` commands are the pipeline's **only correctness gate**, and they
are **operator-supplied**. The pipeline is exactly as correct as the commands
you hand it. If they are narrower than CI, the run ships a change CI then
rejects.

**Real incident:** a `check.lint` scoped to a hand-picked path list — narrower
than CI, which lints the whole tree — passed five runs, and shipped a change
whose lint then **failed in CI**. The green was a lie: the gate never looked
at the file that broke.

- **Seed `check.*` from the repo's own CI workflow commands**, verbatim.
  Never a hand-picked path subset.
- Commands must **PRINT their diagnostics** to stdout/stderr — the fix agent
  is handed that output. A silent `test -z "$(gofmt -l .)"` leaves it blind;
  use `gofmt -l .` so the offending paths show.

## Human gates

`wait.human` nodes (plan-build-review's `plan_gate` + `ship`; the ship gates
in amend-pr/revise-pr) block until answered.

- `--human approve` — auto-approve every gate. Hands-off / unattended.
- `--human console` — answer interactively on the TTY (choice + note).
- `--human auto` (default) — console iff stdin is a TTY, else auto-approve.
- With `--ui`/`--announce`, gates are answerable over the run's `/answer`
  endpoint.

An unattended dispatch of a gated pipeline needs `--human approve` or a
`--ui` answer channel, **or it blocks**.

**Plannotator plan-review bridge** — review a plan in a browser before
approving:

```
scripts/plannotator-gate.sh BASE_URL LOGS_DIR [GATE_NODE] [PLAN_NODE]
# e.g. scripts/plannotator-gate.sh http://127.0.0.1:8080 ~/.attractor/runs/<id>
```

Polls the live run's `/questions`; when it parks at the plan gate, extracts
the plan from the plan span's `status.json`, opens it in Plannotator, and
posts the decision back: approve → `[A]`, annotate → `[R]` with the
annotation as `human.note`, dismiss → left pending (the UI gate still works).
Needs `curl`, `jq`, `plannotator` on PATH. Defaults: `GATE_NODE=plan_gate`,
`PLAN_NODE=plan`.

## Remote viewing

- `--ui` **auto-binds loopback plus the tailnet IP** (any `100.64.0.0/10`
  interface) with zero config — so a run is watchable from another machine
  over Tailscale, tokenless on the tailnet. Both URLs print to stderr. A
  public/LAN `--ui-addr` instead requires `--ui-token`.
- `attractor runs [--root <dir>]` — list local runs from the runs root
  (`$XDG_DATA_HOME/attractor/runs` or `~/.attractor/runs`): id, graph,
  status, start time, most-recent first.
- `attractor view <dir>` — re-serve a **finished** run from its directory,
  read-only, over the same loopback + tailnet binding. Gates return 409
  (no engine attached). `--no-tailnet` keeps a sensitive run loopback-only.

Typical: `attractor runs` to find the id, then `attractor view
~/.attractor/runs/<id>`.

## Worked example — dogfood plan-build-review on this repo

```bash
./result/bin/attractor run --ui --cwd $PWD --stylesheet pipelines/models.css \
  --logs ~/.attractor/runs/<change> \
  -var brief="Implement <milestone> from docs/<spec>.md: …" -var base=main \
  -var "check.deps=nix develop -c true" \
  -var "check.typecheck=nix develop -c go vet ./..." \
  -var "check.lint=nix develop -c gofmt -l ." \
  -var "check.test=nix develop -c go test ./... -race -count=1" \
  plan-build-review
```

`open_pr` opens a draft PR and revise-pr/amend-pr push the branch when the
ship gate passes — shipping performs a GitHub write. Confirm intent before
shipping a side-effecting run.
