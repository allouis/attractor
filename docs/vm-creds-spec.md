# VM runner: LLM credential delivery

An agent (`codergen.acp`) node running **inside a VM** needs LLM credentials, or
it fails at `session/new` with "authentication required". The VM image bundles
the acp adapters (`claude-agent-acp`, `codex-acp`) but ships **no** credentials —
so before this, only *tool* nodes (pnpm/lint/test/jj) worked in a VM; any
agent-driven pipeline with `runner: vm` broke at the first agent step.

## Design

The daemon user authenticates on the host with oauth (`claude login` / `codex
login`), which writes `~/.claude/.credentials.json` and `~/.codex/auth.json`.
Deliver **those files** into the guest:

- **Launcher** (`stageAgentCreds`, `internal/server/launcher_vm.go`): copies only
  the credential files into a per-run `<runDir>/creds` dir, preserving their
  path relative to `~` (`.claude/.credentials.json`, `.codex/auth.json`, and the
  `gh` token `.config/gh/hosts.yml`). The dir is always created — empty if the
  host isn't logged in — so the module's static share always has a valid source.
  Passed as `ATTRACTOR_CREDS_DIR`.

  The `gh` token enables an **in-workflow** `gh pr create` / push node so a
  workflow that produces a PR keeps that logic in its graph. The image ships
  `gh`, and the runner runs `gh auth setup-git` + an `insteadOf` rewrite so
  github pushes go over HTTPS with the token (covers ssh-remote repos too).
  **Trust note:** the `gh` token has `repo` (read+WRITE) scope and lives in the
  guest, so in-guest generated code can read it (`gh auth token`) — the same
  trust level as the LLM token already there. Scoping push creds away from the
  agent is future work (per-node host placement).
- **Module** (`nix/vm-runner.nix`): a ro 9p share `creds → /mnt/creds`. The
  runner script exports `HOME=/root` and `cp -a /mnt/creds/. $HOME/`, so the
  adapter (spawned by `attractor`) finds `~/.claude` / `~/.codex` in-guest.
  Copied (not read off the ro mount) so an in-guest oauth **token refresh** — a
  write back to `.credentials.json` — lands on guest ext4.
- **Backend** (`nix/vm-runner.nix`): the runner invokes `attractor run
  --backend acp`. `attractor run` DEFAULTS to the simulation backend (every
  `codergen.acp` node a no-op), so a VM run — always real — must force ACP. The
  specific adapter comes from the pipeline's graph/node `acp_command`. Without
  this the cred-mount is never even exercised (the agent node never spawns).

## Security

Only the **credential files** cross into the VM — never the whole `~/.claude`
(history, memory, projects, MCP config). Untrusted in-guest code sees the oauth
token (unavoidable: it's what the adapter authenticates with) and nothing else.
The token is the Claude Max / Codex account key, so the mitigations that make
this acceptable are the VM boundary itself: **per-run ephemeral guests** and
**default-deny egress**. A stronger posture (token stays on the host, guest
reaches an egress broker over a scoped path) is future work; this ships the
credential mount the operator opted into so agent pipelines run in VMs now.

## Running a full pipeline in the guest

A VM run executes the whole graph **inside the guest** — the daemon boots the
VM and a fresh `attractor run source.dot` runs there, and a
`stack.manager_loop` spawns its child engine in-process *in the guest*. So the
guest needs everything that run touches:

- **Sub-pipelines.** `writeJob` copies the whole pipeline **catalog** (this
  pipeline AND its siblings) into `/mnt/job`, and the guest sets `--base-dir
  /mnt/job/<pipeline>` — so a manager_loop child like `../review-core` resolves
  to a delivered sibling. Without it, a review/implement pipeline dies in-guest
  with "no such file: ../review-core/pipeline.dot".
- **Provider routing.** `stageProviderConfig` delivers the daemon's
  `default_provider` + `providers` (which acp command each llm_provider maps to)
  to `$HOME/.attractor/config.json`, so the guest routes each codergen node by
  its `llm_provider`/`llm_model` (e.g. review-core's five lenses: four anthropic
  + one codex). ONLY the provider section crosses — no repos, no secrets. The
  guest runs `attractor run` with **no `--backend`** so this routing applies; a
  run-wide `--backend acp` would force one command and break multi-provider
  pipelines, and missing config silently routes to simulation.

## Verification

`pipelines/vm-agent-smoke/pipeline.dot`: one `codergen.acp` node writes a
sentinel file, a `tool` node asserts it. Run it through a daemon started with
`--runner vm` against a jj-colocated repo → the agent node reaching `completed`
proves the adapter authenticated inside the guest.

`pipelines/review-pr` against a Ghost PR (Ghost is `runner=vm`) exercises the
full path: the manager_loop child (`../review-core`) resolves, its five lenses
route to their providers, and `synth` returns a verdict — all in-guest.
