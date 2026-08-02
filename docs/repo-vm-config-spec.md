# Per-repo VM config spec

Let each registered repo declare the **runtime environment it develops in**
— a named launcher (in-process / subprocess / VM) and, for a VM, which
baked image — so dispatching *any* workflow against that repo lands in the
right place automatically, with no per-run flags to remember.

Status: **design proposal** (decision record below, my best judgment —
open to correction). Builds directly on the nix-vm-runner work
(`docs/nix-vm-runner-spec.md`) and the central config
(`docs/config-screen-spec.md`); the milestone ledger is the execution
contract.

## Motivation

The VM runner work gave us three placements — `direct` (in-process),
`local` (subprocess phone-home), `vm` (NixOS VM phone-home) — selected two
ways today: the daemon default (`serve --runner`) and a per-submission
`runner` field (`launcherFor(placement)`, `launcher.go:22`). A VM's
toolchain lives **in the image** (Node/TS or Python baked, D8), and the
daemon points at exactly one image via `--vm-runner <boot-script>`.

That's global. But *which* environment a run needs is a property of **the
repo**: Ghost wants the Node/TS VM, a Python service wants the Python VM,
a docs repo is fine in-process. Encoding that per dispatch means every
launch must remember `runner=vm` **and** the right image — and the daemon
can only hold one image at a time. A repo should **declare its dev
environment once**, centrally, and every dispatch against it should place
correctly. The VM spec already anticipated this: *"a future `runtime`
field selects the image"* (nix-vm-runner-spec §Runtimes).

## Decision record (proposed)

| Topic | Decision |
|---|---|
| **Where it lives** | In the **central config** `repos.<name>` block (extends `config-screen-spec`). A repo gains `runner` (named launcher) and, for `vm`, a `vm.image` (named image). One config document. |
| **Named image registry** | Generalise today's single `--vm-runner` into a **name → boot-script** map (`vm_images` in config, and/or repeatable `--vm-runner name=path`). A repo's `vm.image` references a name; the old single flag becomes the `default` entry. |
| **Per-run image on the VM launcher** | `NewVMLauncher` fixes one script today. Make `Launch` resolve the image **per run** from the registry, defaulting to the daemon's default image. No protocol change — a different image is just a different `run-nixos-vm`. |
| **Resolution precedence** | At dispatch: **submission override > repo config > daemon default**, evaluated for both runner (`launcherFor`) and image. So a repo's declared placement is the default, still overridable per run for testing. |
| **Image lifecycle stays out** | Building/registering images remains a nix/CLI concern (`nix build .#vm-runner`, `--vm-runner name=path`). v1 does **not** build images from the UI — the config only *references* registered images by name. |
| **UI surface** | The config Repos panel (config-screen C4) gains a **runner** select and, when `vm`, an **image** select populated from the registered `vm_images`. Read-mostly for team members; the daily driver is "this repo → this VM." |

## Data model (extends `config.json`)

```jsonc
{
  "vm_images": {                         // name -> run-nixos-vm boot script / flake ref
    "default": ".#vm-runner",            // today's single --vm-runner becomes this
    "node-ts": ".#vm-runner",
    "python":  "/nix/store/…-run-nixos-vm-python"
  },
  "repos": {
    "TryGhost/Ghost": {
      "path": "/home/agent/Ghost",
      "checks": { "deps": "…", "test": "…" },
      "runner": "vm",                    // direct | local | vm  (default: daemon --runner)
      "vm": { "image": "node-ts" }       // only meaningful when runner=vm; default: "default"
    }
  }
}
```

## Dispatch resolution

At submit, once the item/form resolves `repo → cwd`, also resolve
placement:

1. **runner** = `submission.runner` else `repos[repo].runner` else daemon
   `--runner` default.
2. **image** (only when runner=`vm`) = `submission.image` else
   `repos[repo].vm.image` else `"default"`.

`launcherFor(runner)` already picks the named launcher; the VM launcher
gains the resolved image. An unknown runner or image is a clear submit
rejection (list the registered names), consistent with config-screen's
reject-structural validation.

## Milestones

| # | Deliverable | Depends on | Status |
|---|---|---|---|
| VM1 | Named-image registry: VM launcher resolves the boot script **per run** from a name→script map (default = today's single script); `vm_images` config + repeatable `--vm-runner name=path`; tests | nix-vm work | todo |
| VM2 | `repos.<name>` gains `runner` + `vm.image` in the central config schema (part of the config-screen document); projection into dispatch; tests | config-screen C1 | todo |
| VM3 | Dispatch resolves runner+image with precedence (submission > repo > default) and rejects unknown names; wired through submit; tests | VM1, VM2 | todo |
| VM4 | Config Repos panel: per-repo **runner** select + **image** select (from registered `vm_images`); save via `PUT /config` | VM3, config-screen C4 | todo |

## Non-goals (v1)

- Building or garbage-collecting images from the UI (nix/CLI only).
- Per-run *ad-hoc* images not in the registry.
- Auto-inferring a repo's runtime from its contents.
- Resource sizing (CPU/RAM/disk) per repo — one VM shape for now.
