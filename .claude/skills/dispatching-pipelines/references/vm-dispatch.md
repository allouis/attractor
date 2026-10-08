# Dispatch pipelines into Ghost VMs

Use the companion `ghost-vm` checkout (normally `~/ghost-vm`). Its `README.md`
and `docs/hub-and-workspaces.md` are the authoritative launcher/runbook docs.
Inspect the actual pipeline before dispatching: model providers, variable
contracts, dependencies, gates and publishing steps still determine the run.

## Launch and announce

Prerequisites: Linux x86_64 with KVM and Nix, a prepared Ghost checkout, a
Nix-wrapped Attractor binary and the selected model credentials. Set
`GHOST_VM_REPO` to the intended source checkout and `GHOST_VM_ATTRACTOR` to
the wrapped binary, not a bare Go build. Use the existing shared hub base URL
(on this host, normally `http://127.0.0.1:7799`); do not start another hub over
its port or state directory. `--announce` takes the base URL, without `/ui`.

Example for a new feature workspace using the bundled local-only pipeline:

```bash
export GHOST_VM_REPO="$HOME/ghost-run"
export GHOST_VM_ATTRACTOR="$HOME/attractor/result/bin/attractor"
mkdir -p "$HOME/vm-workspaces"
cd "$HOME/ghost-vm"
nix build .#ghost-vm --out-link "$HOME/vm-workspaces/my-feature-launcher"

"$HOME/vm-workspaces/my-feature-launcher/bin/ghost-vm" run \
  "$HOME/ghost-vm/pipelines/local-change/pipeline.dot" \
  --workspace "$HOME/vm-workspaces/my-feature" \
  --output "$HOME/vm-results/my-feature-1" \
  --auth codex --announce http://127.0.0.1:7799 --timeout 7200 -- \
  -var brief='Describe the requested Ghost change' \
  -var check='pnpm check'
```

This particular pipeline takes `brief,check` and has no publishing or human
gate nodes. Other pipelines retain their own contracts, including `check.*`;
keep checks aligned with the repository's CI as instructed in `SKILL.md`.
Use the user's intended feature name, source checkout and checks.

- Launcher flags go **before `--`**. Attractor flags and `-var` values go after.
  The launcher owns `--cwd`, `--logs`, `--announce`, `--ui`, `--ui-addr` and
  `--ui-token`; do not pass these through. Guest working directory is `/work/repo`.
- Use `--auth codex|claude|both|none` for the actual providers. Shared model
  stylesheets can select both providers, requiring `--auth both`. Retain the
  no-`--backend` rule for stylesheet routing.
- Output must be new on every run, outside the source and workspace directories.
  Keep the launcher attached to a managed terminal/job until completion; exiting
  or interrupting it can terminate the VM.

## Pipeline files and starting revision

The launcher currently takes an **existing DOT file path**, not a bare pipeline
name. It copies only that file's parent directory to
`/mnt/runtime-config/pipeline`. Sibling directories outside it are not copied.
Before using standard pipelines, make a self-contained variant containing all
referenced subgraphs, prompts and stylesheets, with their references adjusted.
Do not launch a standard pipeline that references `../checks-core` or
`../review-core` and assume the launcher copies those siblings.

Paths passed after `--` must exist inside the VM. For a stylesheet packaged
beside the DOT file, use `--stylesheet /mnt/runtime-config/pipeline/models.css`.
Host `~/...` paths passed as vars are not translated into guest paths.

A new workspace imports the selected source checkout's `jj @`; reuse keeps
its existing guest state. `workspace_revision` remains a declared pipeline var,
not an instruction to check out a revision. Materialize the intended PR revision
before seeding a new workspace, or use an explicit guest preparation step.
Check the pipeline's base references: `base@origin` needs that remote ref fetched,
and host revset aliases such as a configured `trunk()` are not imported. Set up
the actual review base explicitly; authentication alone does not prepare it.

## Hub, gates and review

Open the shared hub's `/ui`. The launcher announces after the guest pipeline
starts, forwarding the guest API with its temporary bearer token. The hub
proxies live status and human answers. Inspect `OUTPUT/console.log` during boot,
then `OUTPUT/hub.json`, `OUTPUT/hub.log` and `OUTPUT/run/events.jsonl`.

For browser human gates, supply launcher `--announce` and **leave `--human`
unset**. `--human approve` explicitly bypasses all gates; use it only when the
requested workflow is unattended. `--human console` cannot work with the VM
launcher's disconnected stdin. Gate waiting counts against `--timeout`.

For a hub on another machine, it must reach the forwarded API on the VM host.
Use `--api-bind VM_HOST_PRIVATE_IPV4` before `--` and a reachable private network.
`--api-url` is only for an already-configured reverse proxy; it does not create
one. See the companion hub runbook before changing networking.

`scripts/plannotator-gate.sh` works for VM runs **through the hub**: it
reads everything over the run's HTTP API (the hub proxies questions,
artifacts and answers to the guest with its bearer token) and takes an
explicit run id — ambiguous listings are refused:

```bash
scripts/plannotator-gate.sh http://127.0.0.1:7799 <run-id>
```

Arm it after the launcher announces (the run id is in the launcher
output and the hub listing). Plan review then happens in Plannotator
exactly as for local runs; the hub UI gate stays available as the
fallback. Answer gates promptly: the guest's model-credential snapshot
goes stale after roughly an hour of host activity, and gate waiting
counts against `--timeout`.

## GitHub and subsequent feedback

For a pipeline that needs GitHub, add `--github-auth host` before `--` on each
run. The launcher stages the active host token in the temporary read-only
runtime mount, sets guest `GH_TOKEN`, installs `gh` and a Git HTTPS credential
helper, and restores the source repository's `github.com` origin. SSH origins
become HTTPS. Credential precedence is host `GH_TOKEN`, `GITHUB_TOKEN`, then
`gh auth token --hostname github.com`; no SSH forwarding is needed.

This exposes the existing token and its full permissions to trusted guest code.
It supplies capability, not additional authorization. Keep pushes and PR changes
within the user's requested workflow and explicit pipeline steps. Adding this
flag does not add publishing to `local-change`. Prefer deterministic tool nodes
for push/PR commands, with agents producing the code and review text.

For reviewer feedback, use the same named `--workspace`, its original built
launcher and a **new** `--output`. Supply the feedback or have the pipeline read
it with `gh`; reuse the feature bookmark and existing PR. Do not silently start
a fresh implementation from the original base and discard earlier VM work.
The guest checkout, commits, dependencies and MySQL survive; the VM boots fresh
and credentials must be supplied again. Only one run can own a workspace.

Workspace metadata pins the source path/revision, dependency store and VM image.
Keep its built launcher; rebuilding over it can select an incompatible image.
An older image cannot gain GitHub support by passing the new flag: recover its
code and seed a new workspace with the updated launcher. Reuse does not fetch
remote edits automatically. If someone else updated the PR branch, explicitly
fetch/reconcile it or seed a new workspace from the reviewed remote revision.

## Results and recovery

`OUTPUT/run/` contains live Attractor logs. Completion exports `changes.patch`
for this run, cumulative `workspace.patch` and `changes.bundle`, and
`result.json`. The host source checkout is not updated. Bundles can be imported
through a local jj remote into a review checkout containing the original source
revision. The latest cumulative bundle includes earlier workspace runs.

With `--announce`, the launcher uploads the completed archive to the hub;
code artifacts appear under `vm/`. This uploads code/logs to the selected hub.
The live run disappears on shutdown; its archive remains viewable.
Exit 124 means timeout; 125 means startup/export failure. Exit 69 with a
successful pipeline means archive delivery failed: inspect `hub.json` and retry
delivery after the launcher exits, without rerunning the pipeline:

```bash
"$HOME/vm-workspaces/my-feature-launcher/bin/ghost-vm" publish \
  "$HOME/vm-results/my-feature-1" --announce http://127.0.0.1:7799
```

Here `publish` uploads a hub archive; it does not push code or create a PR.
