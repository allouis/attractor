# AGENTS.md — operating attractor on a fresh box

This file makes the repo self-sufficient: an agent (or human) with just a
clone can bring up attractor, configure it, and run pipelines — including
the self-dev loop that builds attractor with attractor.

**VCS rule (important):** use **`jj` (Jujutsu), never `git` directly**, for
all version-control operations in this repo and in the repos pipelines
operate on. Small, atomic, reviewable commits. (If a personal `CLAUDE.md`
with additional style rules is present in your home dir it also applies,
but it does not ship with this repo — this runbook is the source of truth
for operating attractor.)

attractor is a DOT-pipeline AI-workflow runner (Go). The unit of the
system is a **self-contained run**: `attractor run --ui` executes a
pipeline and serves its own live API + waterfall view; `attractor hub` is
the optional pull-based directory (announce + scrape + archive + launch);
`attractor serve` is the legacy daemon (registry + web UI + item intake +
VM launchers). Read the README's "How a run works" section first — it is
the mental model (run dir layout, engine loop, loop guards, agent
status.json contract) everything here builds on.

## 1. Prerequisites / toolchain

- **nix** (flakes) — builds the binary and the VM runner image; the dev
  shell provides Go, gofmt, etc.
- **jj** (Jujutsu) — VCS for this repo and for the repos pipelines operate
  on. Never use `git` directly (see `CLAUDE.md`).
- **claude-agent-acp** — the ACP agent the `acp` backend drives. Must be on
  `PATH`. (`which claude-agent-acp` should resolve.)
- **A secrets env file** (`~/.secrets.env`) exporting at least:
  - `ANTHROPIC_API_KEY` — for the agent.
  - `GH_TOKEN` — GitHub item source (`gh`).
  - `LINEAR_API_KEY` — Linear item source (config falls back to this env
    var when `config.json` sets no key).
- For **VM runs** (this box's purpose): KVM (`/dev/kvm` present) + the nix
  VM runner image (built from this flake). See §7.

## 2. Bring-up (fresh box)

```bash
# clone + get the working branch
git clone <origin> attractor && cd attractor
jj git init --colocate        # if operating with jj
jj git fetch && jj new build-out-goal   # or the merged branch

# build the daemon and the VM runner image
nix build .#attractor         # -> ./result/bin/attractor
nix build .#vm-runner         # -> the run-nixos-vm boot script (VM runs)

# secrets
source ~/.secrets.env

# config (see §3) and catalog (see §5) then run something
./result/bin/attractor run hello --var name=world   # simulation, no agent
```

`nix build .#vm-runner` repoints `./result`; build the daemon to its own
out-link if you need both at once:
`nix build .#attractor --out-link result-attractor`.

## 3. Config — `~/.attractor/config.json`

The daemon **owns** `~/.attractor/config.json` (schema in
`docs/config-screen-spec.md` / `internal/config/document.go`). Both
`attractor run` and `serve` read it; the web UI's Config tab edits it. No
migration from old TOML — write it fresh (or set it in the UI).

Registers providers, the Linear key (or rely on the env var), a VM-image
registry, and repos with their check commands + VM placement. **Adjust the
`path`s to where the checkouts actually live on this box.**

```jsonc
{
  "default_provider": "anthropic",
  "providers": {
    "anthropic": { "backend": "acp", "command": "claude-agent-acp", "model_env": "ANTHROPIC_MODEL" }
  },
  "linear": { "api_key": "" },              // empty -> LINEAR_API_KEY env is used
  "vm_images": { "default": ".#vm-runner" },
  "repos": {
    "allouis/attractor": {
      "path": "/ABS/PATH/TO/attractor",
      "checks": {
        "deps":      "nix develop -c go mod download",
        "typecheck": "nix develop -c go vet ./...",
        "lint":      "test -z \"$(nix develop -c gofmt -l .)\"",
        "test":      "nix develop -c go test ./..."
      }
    },
    "TryGhost/Ghost": {
      "path": "/ABS/PATH/TO/Ghost",
      "checks": {
        "deps": "pnpm install --frozen-lockfile",
        "lint": "pnpm run lint",
        "test": "pnpm run test"
      },
      "runner": "vm",
      "vm": { "image": "default" }
    }
  }
}
```

Generate it with real paths in one shot (edit the two paths first):

```bash
ATTRACTOR_PATH=$(cd . && pwd)          # this repo
GHOST_PATH=$HOME/Ghost                 # adjust
mkdir -p ~/.attractor && chmod 700 ~/.attractor
# paste the JSON above with the two /ABS/PATH placeholders replaced, then:
chmod 600 ~/.attractor/config.json
# sanity-check it loads:
./result/bin/attractor serve --bind 127.0.0.1:7799 & sleep 3
curl -s 127.0.0.1:7799/config | python3 -m json.tool      # secrets redacted
curl -s 127.0.0.1:7799/repos                              # should list both
```

**Notes on the two repos:**
- **attractor** — the checks are the project's real gate and run **on the
  host** (`direct` runner; no `runner` key needed). Good dogfood target.
- **Ghost** — `lint` and `test:unit` need no database; the **full `test`
  provisions MySQL via Docker**, so Ghost is set to `runner: "vm"` (the VM
  image ships Docker + pnpm). Two known constraints, both tracked in
  `docs/vm-workspace-spec.md`: (1) Ghost pins **pnpm 11.8** via
  `packageManager` — use **corepack** (`corepack pnpm …`) so the pinned
  version is used, not the image's nixpkgs pnpm; (2) the current rw **9p**
  workspace mount **breaks SQLite** (`pnpm install` fails) — the
  vm-workspace spec (virtiofs) is the fix. Until that lands, Ghost's
  full dockerized suite won't pass in a VM; `lint` / a small package test
  are the demonstrable checks.

## 4. Running a pipeline

Name resolution (`resolvePipelinePath`, `internal/cli/cli.go`):
1. `./pipelines/<name>/pipeline.dot`  (project-local — works from the repo)
2. `./pipelines/<name>.dot`
3. `~/.attractor/pipelines/<name>/pipeline.dot`  (the catalog)
4. `~/.attractor/pipelines/<name>.dot`

A path with `/` or a `.dot` suffix bypasses the lookup. So from the repo
you can `attractor run implement`; elsewhere you need the catalog (§5).

Backend selection: with no `--backend` flag, each codergen node is
**routed by provider config** (§3) — the node's `llm_provider` /
`llm_model` attrs pick a provider from `~/.attractor/config.json`
(falling back to `default_provider`), and the model is injected via the
provider's `model_env`. This is the normal path; pipelines pin models
this way. Caution: with **no** config.json the router silently falls
back to simulation. `--backend simulation|acp|claude` (+ `--acp-cmd`)
are run-wide debugging overrides that **bypass routing** — every node
runs on the one command, ignoring `llm_*` pins. Vars: `--var key=value`
per the graph's `vars=` declaration. Logs: `--logs <dir>`.

```bash
# real agent run of the implement pipeline against a registered repo
# (provider-routed; needs §3 config.json)
source ~/.secrets.env
./result/bin/attractor run \
  --var repo=allouis/attractor --var identifier=... --var title=... \
  --logs ~/.attractor/runs/demo pipelines/implement/pipeline.dot
```

## 5. Catalog (`~/.attractor/pipelines`) — needed for `serve` + by-name

The daemon's `/workflows` catalog and by-name resolution outside the repo
read `~/.attractor/pipelines/`. Populate it with symlinks to the repo so
there's one source of truth:

```bash
mkdir -p ~/.attractor/pipelines
for p in bug-fix implement review-core review-pr router; do
  ln -sfn "$(pwd)/pipelines/$p" ~/.attractor/pipelines/$p
done
# examples (hello etc.) live in ./examples
```

## 6. The self-dev / dogfood loop

Features here are built **by attractor**: write a spec under `docs/` with a
**milestone ledger** (a table with a Status column), then run the
`build` pipeline once per milestone. It does
plan → implement (TDD) → verify (gate) → **review-core 5-lens loop** →
record (commits atomically via jj, flips the milestone to `done`).

```bash
BASE=$(jj log -r @- --no-graph -T change_id | tr -d '[:space:]')
./result/bin/attractor run \
  --var spec=docs/<spec>.md --var review_base=$BASE \
  --logs ~/.attractor/runs/<milestone> pipelines/build/pipeline.dot
```

`review_base` is the change id **before** this run's first milestone commit
(`@-`); advance it each run. The review lenses diff `--from $review_base
--to @`. Gate = `nix develop -c go test ./... -race` + gofmt + `nix build`.
See `docs/build-goal-log.md` for a worked history of ~180 commits built
this way, and any interventions.

## 7. Running in VMs (this box's purpose)

VM runs execute the pipeline inside a per-run NixOS VM that phones home to
the daemon. Start the daemon with a VM launcher:

```bash
BOOT=$(nix build --no-link --print-out-paths .#vm-runner)/bin/run-nixos-vm
source ~/.secrets.env
./result/bin/attractor serve --bind 127.0.0.1:7799 \
  --runner local --vm-runner "$BOOT" --vm-dir ~/.attractor/vms \
  --logs ~/.attractor/runs
# `--runner local` keeps host runs default (subprocess; the in-process
# direct runner is retired); the vm launcher is available
# for repos that declare `runner: vm` (e.g. Ghost) or a per-run override.
```

The VM image bakes attractor, node/pnpm, python, **Docker + compose**, and
mounts the repo. **The current workspace delivery is rw 9p and is being
replaced by virtiofs — read `docs/vm-workspace-spec.md` and
`docs/vm-workspace-prompt.md` (the acceptance-driven task) before doing VM
workspace work.** On this KVM box, VMs run near-native (unlike a TCG dev
box); `-machine accel=kvm:tcg` auto-selects KVM when `/dev/kvm` exists.

## 8. Gotchas (hard-won — distilled so they survive off any one box)

- **Build fresh from source.** A stale installed `attractor` (e.g. an old
  `nix profile`) may drop dotted attrs like `stack.child_dotfile`. Use the
  `nix build` output.
- **`serve` needs secrets in its env** — launch after `source ~/.secrets.env`
  or GitHub/Linear return 401 and agent runs have no API key.
- **Don't pass `--backend`/`--acp-cmd` for normal runs** — they bypass
  provider routing, so per-node `llm_model`/`llm_provider` pins (build's
  Fable plan/implement, review-core's Opus lenses + codex correctness
  lens) are ignored and every node runs on the one command.
- **`nix build .#vm-runner` repoints `./result`** — use a separate
  `--out-link` if you need the daemon binary too.
- **Killing processes:** `pkill -f <pattern>` can match the killing shell
  itself; prefer exact PIDs.
- **VM tool-node stdout isn't surfaced** to the daemon today (only
  pass/fail) — to debug a VM tool failure, redirect output to the job share
  (`… > /mnt/job/out.log 2>&1`) and read it on the host.
- **Ghost + VMs:** see §3 — pnpm version pin (corepack) and the 9p/SQLite
  blocker (virtiofs fix pending).

## 9. Where things live / pointers

- Binary: `./result/bin/attractor` (after `nix build .#attractor`).
- Config: `~/.attractor/config.json` (§3). Runs: `~/.attractor/runs/`.
  Catalog: `~/.attractor/pipelines/` (§5). VM disks: `--vm-dir`.
- Web UI: `serve` then `http://127.0.0.1:7799/ui` (4 tabs incl. Config).
- Specs: `docs/` — start with `config-screen-spec.md`,
  `repo-vm-config-spec.md`, `web-ui-v2-spec.md`, `run-workflow-spec.md`,
  `nix-vm-runner-spec.md`, and the current focus
  `vm-workspace-spec.md` + `vm-workspace-prompt.md`.
- Build history + interventions: `docs/build-goal-log.md`.
