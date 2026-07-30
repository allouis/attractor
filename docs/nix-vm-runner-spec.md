# Nix VM runners

Run a pipeline inside an isolated NixOS VM the daemon provisions itself. The
user says "run this workflow"; config decides where it runs (local process /
VM); the user never touches a VM or a shell.

Builds on the agreed design (see conversation + [decision log](nix-vm-decisions.md)):

- **HTTP phone-home everywhere.** The process running the engine POSTs events,
  uploads artifacts, and polls for control (cancel + human answers). One
  transport for local-subprocess, VM, and future remote. Daemon = control
  plane + ingest; it no longer runs the engine in-process.
- **Launcher seam.** The daemon provisions the execution environment. Impls:
  `local` (fork/exec, loopback) and `vm` (QEMU/NixOS). Selected by config —
  global default + per-run override.
- **Workspace.** `local` uses the existing checkout. `vm` mounts the host
  checkout in (9p/virtiofs) to start; clone-per-run is opt-in and only
  *required* once we go remote (no host to mount).

## Why the VM is the isolation unit

Nodes hand off work through the **working tree** (repo), not just Context. The
repo is the coupling; a per-node VM would force workspace sync on every hop.
So the unit of isolation is the **run**. One VM per run.

## Environment constraint (this host)

No nested virt (`systemd-detect-virt=kvm`, CPU exposes only `hypervisor`).
QEMU runs under **TCG software emulation**. Launcher auto-detects `/dev/kvm`
and prefers `-accel kvm`, falling back to `-accel tcg`. Slower, correct.

## Architecture

```
 user → daemon (control plane + ingest)
          │  submit(RunSpec)
          ▼
      Launcher.Launch(RunSpec) ──► provisions env, starts:
          │                          attractor run --report-to http://HOST:PORT
          │                                          --run-id ID --token T
          │  local: fork/exec on 127.0.0.1
          │  vm:    QEMU NixOS guest; HOST = 10.0.2.2 (qemu user-net)
          ▼
      child engine ──HTTP──► POST   /pipelines/{id}/events      (stream)
                             POST   /pipelines/{id}/artifacts/… (upload)
                             GET    /pipelines/{id}/control      (poll: cancel + answers)
```

The daemon persists what it receives (its own `events.jsonl` + artifacts) and
serves it exactly as today (`GET /events` SSE, `GET /artifacts`, `GET /stages`).
The child owning its own run dir on its own FS is irrelevant to the daemon.

## Human gates over HTTP

`wait.human` in the child emits an `interview_started` event (carries the
question + options). The daemon, ingesting that event, registers a pending
question — reusing the existing `POST /questions/{qid}/answer` surface. The
human answers; the child polls `GET /control`, receives the answer, feeds it
to its interviewer. No new question API on the daemon side.

## Phases & milestones (TDD, atomic commits)

### Phase 1 — Phone-home reporting protocol (additive; no VM) ✅
- **V1 ✅** Daemon ingest: `POST /pipelines/{id}/events` accepts one `engine.Event`
  (run-token auth), appends to history + fans out to SSE + persists.
- **V2 ✅** Daemon control: `GET /pipelines/{id}/control` returns `{cancel: bool,
  answers: {qid: label}}`. (answers wired with the poll interviewer, Phase 4.)
- **V3 ✅** `report.Client` event forwarder: drains an engine event stream, POSTs each.
- **V4 (deferred → Phase 4)** poll interviewer. Report mode uses AutoApprove (D4).
- **V5 ✅** Artifact upload: `POST /pipelines/{id}/artifacts/{path…}`; child uploads
  its stage dir on completion.
- **V6 ✅** `attractor run --report-to URL --run-id ID --report-token T`: wires V3+V5.

### Phase 2 — Launcher seam + `local` ✅
- **V7 ✅** `Launcher` interface; dispatcher routes through it; runs self-complete
  from the ingested terminal event. Default `direct` (in-process) kept (D5).
- **V8 ✅** `localLauncher`: spawns `attractor run --report-to 127.0.0.1:PORT`,
  blocks on exit, fails the run if the child dies without reporting completion.
- **V9 ✅** `serve --runner direct|local`. **Dogfooded:** HTTP submit under
  `--runner local` ran the tool in the working tree and streamed full lifecycle
  events + uploaded stage artifacts back to the daemon.

### Phase 3 — `vm` launcher (the goal) ✅
- **V10 ✅** NixOS runner image (nix/vm-runner.nix), headless serial, autologin.
- **V11 ✅** Workspace-in: 9p `workspace` share (rw) of the host tree at
  /mnt/workspace, `job` share (job.json + source.dot) at /mnt/job (D6).
- **V12 ✅** Guest oneshot runs `attractor run --report-to 10.0.2.2:PORT …`
  on the shared workspace over qemu user-net (D7).
- **V13 ✅** `vmLauncher`: writes job, boots QEMU (accel auto via runner
  script), waits for terminal via phone-home, leaves the VM running.
  **Dogfooded:** real daemon `--runner vm` ran a pipeline in a real VM.
- **V14 ✅** Node/TS example (examples/node-ts) run end-to-end in a VM:
  npm install → tsc → node → node --test, all green.

### Phase 4 — Fleshing out
- **V15** Base images with prefilled FS: pre-seed the nix store / npm cache /
  git so runs start fast. Documented recipe.
- **V16** Multi-runtime examples: Node/TS, Python, Go — how to declare a
  runtime's deps in the VM image.
- **V17** VM lifecycle: VMs persist N days after completion; a reaper GCs old
  ones. Config for retention.
- **V18** Per-run placement override in the run modal / submit API.

## Runtimes: how apps of different kinds run in a VM (V16)

App runtimes live in the **image**, not in per-run setup (decision D8). A
tool node's command runs in the guest with `/run/current-system/sw/bin` on
PATH, so anything in `environment.systemPackages` (nix/vm-runner.nix) is
available to pipelines. The default image ships **Node + TypeScript**
(`nodejs_22`, `typescript`) and **Python** (`python3`).

**Add a runtime:** append its nixpkgs package(s) to `systemPackages` in
`nix/vm-runner.nix`, rebuild `.#vm-runner`, point the daemon at it. Examples:

| Runtime | Add to systemPackages | Pipeline stages |
|---|---|---|
| Node/TS | `nodejs_22`, `typescript` | `npm install` → `tsc` → `node` → `node --test` |
| Python | `python3` (+ `uv`/`poetry` for deps) | `py_compile` → `unittest`/`pytest` → `python app.py` |
| Go | `go` | `go build ./...` → `go test ./...` |
| Rust | `cargo`, `rustc` | `cargo build` → `cargo test` |

Worked examples: `examples/node-ts/` (deps + tsc + tests) and
`examples/python/` (stdlib unittest, no pip). Each has a `pipeline.dot`
submitted with `cwd` = the app tree; the tool nodes run in the VM's
`/mnt/workspace`.

**Deps that need the network** (`npm install`, `pip install`) work over the
VM's user-net. To make them fast/offline, pre-seed the base image (V15).

**Slim / per-runtime images:** keep one image per runtime family by editing
`systemPackages` and building a variant; the launcher points at whichever
`run-nixos-vm` you built (`--vm-runner`). A future `runtime` field on the
placement can select the variant automatically.

## Base images with a prefilled filesystem (V15)

Cold runs pay for dependency downloads (`npm install`, `pip install`) and
first-boot work. Prefill to amortize it:

- **Runtime + toolchain**: already baked via `systemPackages` (D8) — the
  biggest win; node/tsc/python are in the image, not fetched per run.
- **Nix store reuse**: the guest mounts the host `/nix/store` (9p,
  `mountHostNixStore`), so anything the host has built is visible in the VM
  without copying.
- **Dependency caches**: bake a warm package cache into the image by adding
  a derivation that populates e.g. `/var/cache/npm` (npm `cache` dir) or a
  wheel cache, and pointing the tool env at it (`npm_config_cache`,
  `PIP_CACHE_DIR`). Because it is in the read-only image, every run starts
  warm.
- **Prebuilt `node_modules`/venv**: for a fixed app, bake its installed deps
  into the image (or a dedicated 9p `deps` share) so the `deps` stage is a
  no-op.

The mechanism is ordinary NixOS: add packages / `environment.etc` /
`systemd.tmpfiles` rules to `nix/vm-runner.nix`. Nothing in the launcher or
protocol changes — a prefilled image is just a different `run-nixos-vm`.

## RunSpec (draft)

```go
type RunSpec struct {
    RunID        string
    Source       string            // pipeline dot source
    Vars         map[string]string // seeds $context (incl. item.*, check.*)
    Workspace    string            // host path to the working tree
    ItemRef      string
    WorkflowName string
    BaseDir      string            // for @file prompt resolution
    Placement    Placement         // local | vm (resolved: default←override)
}
```

## Testing rules (per goal)

- Red/green TDD, atomic commits.
- Dogfood: actually boot VMs and run apps, not just unit tests.
- Test repos: **local bare repos or private GitHub only**. Nothing public.
- Hard decisions: subagent debate, recorded in [decision log](nix-vm-decisions.md).
