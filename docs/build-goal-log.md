# Build-goal log

Running log for the "build out attractor, using attractor" goal
(2026-08). Deliverables: build the **config screen**, then spec + build
**per-repo VM config** and a **team-ready web UI**. Built by dogfooding
the `build-review` pipeline (plan → implement → verify → 5-lens review-core
loop → record). Small atomic commits; every intervention recorded here.

## Plan

| Spec | Status | Build |
|---|---|---|
| `config-screen-spec.md` | **BUILT (C1–C5 done)** | ✅ |
| `repo-vm-config-spec.md` | drafted | **VM1–VM3 done**; VM4 todo |
| `web-ui-v2-spec.md` | drafted | U1–U7 todo |

Build order: config-screen C1→C5, then VM1→VM4, then web-UI U1→U7. Web-UI
U1/U2/U3 are independent of the config work and can interleave.

Dogfood invocation (one run per milestone; advance `review_base` each run):
```
BASE=$(jj log -r @- --no-graph -T change_id | tr -d '[:space:]')
result/bin/attractor run --backend acp \
  --var spec=docs/<spec>.md --var review_base=$BASE \
  --logs ~/.attractor/runs/<milestone> pipelines/build-review/pipeline.dot
```

## Stack

All work stacked on the `nix-vm-runners` bookmark (VM launcher seam +
phone-home; unmerged to `main`). `main` is stale at the original
config-screen spec commit; advancing it is deferred to the end.

## Timeline

- **Specs prepared.**
  - `rouspyrz` — `docs(config-screen)`: locked grilled decisions into a
    decision-record table (daemon-owned JSON, whole-doc PUT + secret-merge,
    tailnet-trust + redaction, no migration, live checks / restart-note
    providers, reject-structural validation, hand-built panels).
  - `repo-vm-config-spec.md` drafted — per-repo runner+image, named-image
    registry generalising `--vm-runner`, dispatch precedence
    (submission > repo > default).
  - `web-ui-v2-spec.md` drafted — gap analysis vs the shipped 3-tab SPA;
    the debug/gate endpoints already exist server-side and are simply
    unwired; U1–U7 close the "UI-only teammate can work" gap.

- **config-screen C1 — done** (dogfood run `39dd2753d9f1`, `~/.attractor/runs/config-C1`).
  6 atomic commits, gate green (`go test ./... -race` + `nix build .#attractor`):
  - `oyxvzlst` feat(config): Document type with JSON load/save and fresh default
  - `lxununml` feat(config): projections to Config, repos map, per-path checks
  - `tzxksxyt` refactor(cli): read provider config from central config.json
  - `tlrwwmxw` refactor(server): seedChecks reads checks from central config.json
  - `mzxquqkr` refactor(cli): serveRepos projects repos from config.json
  - `rslyzxlq` docs: mark C1 done

  The 5-lens review-core loop ran inline and its synth flagged real issues
  the fix loop addressed before record (map-aliasing on projections, config
  loaded/parsed multiple times in `serve()`, test over-coupling). No human
  intervention needed. Follow-ups the synth left non-blocking: legacy-config
  warning when an old `config.toml` is present; revisit `DefaultDocument`'s
  seeded provider once C4 lands.

- **config-screen C2 — done** (dogfood run, `~/.attractor/runs/config-C2`).
  5 atomic commits, gate green, no intervention:
  - `qqmuzxrx` feat(config): redaction, validation, secret-merge on Document
  - `zwolsplk` feat(server): GET /config redacted + PUT /config secret-merge/validate
  - `owszxnyn` feat(server): GET /repos projects config.repos live
  - `zvtuswqk` test(e2e): config API roundtrip through the real server
  - `nuznosro` docs: mark C2 done

- **config-screen C3 — done** (dogfood run, `~/.attractor/runs/config-C3`).
  5 atomic commits, gate green, no intervention:
  - `tyoznwxv` feat(config): ChecksForRepo keys checks by repo ref
  - `tvrqwlnp` refactor(items): thread the run's repo through the Submit seam
  - `nmzutnum` feat(server): seedChecks keys checks by the run's repo
  - `mrluyknp` feat(config): RepoForPath resolves a repo ref from a checkout path
  - `ruwsulzn` fix(server): seedChecks backfills repo from cwd, warns on unregistered ref
  - `oytxplyv` docs: mark C3 done

- **config-screen C4 — done** (dogfood run, `~/.attractor/runs/config-C4`).
  8 atomic commits, gate green, no intervention:
  - `tqpnrkzm` refactor(ui): drive nav/route from a TABS registry
  - `mkznumys` feat(ui): Config tab with the Repos panel
  - `xxylzwlr` feat(config): honor an explicit Linear key clear on PUT
  - `xlowyuvu` feat(ui): Linear secret panel in the Config view
  - `wmkkulyp` feat(ui): Providers panel in the Config view
  - `snpvtymk` feat(ui): whole-doc save in the Config view
  - `zvtowntq` fix(cli): upload stage artifacts before the terminal phone-home event
  - `koxuqvou` docs: mark C4 done

  **Real bug surfaced + fixed by the dogfood:** the pre-existing
  `TestLocalLauncherEndToEnd` `-race` flake (noted after C1) reproduced 3/8;
  the run root-caused it (artifacts uploaded after the terminal phone-home
  event raced the daemon reading them) and fixed it properly — upload inside
  the forwarding goroutine before the terminal event, post-loop fallback.
  Now 20/20 deterministic under `-race`.

- **config-screen C5 — done** (dogfood run, `~/.attractor/runs/config-C5`).
  6 atomic commits, gate green, no intervention:
  - `skpklrkv` refactor(ui): render the Linear clear-arm from a panel param
  - `myypvuuk` feat(ui): empty states for the Repos and Providers panels
  - `wtktvnoq` feat(ui): surface soft save-warnings inline on the offending repo row
  - `rpotzowu` feat(ui): make the Linear key clear reversible
  - `rowymzqw` feat(ui): guard unsaved Config edits on navigation and unload
  - `qvvrqnwp` docs: mark C5 done

- **✅ config-screen-spec fully built** — C1–C5, 31 atomic commits, gate
  green throughout, zero human interventions, one real `-race` bug fixed en
  route. The daemon now owns `~/.attractor/config.json`; TOML load is gone;
  `GET`/`PUT /config` + `GET /repos` are live; a Config tab edits
  repos+checks / Linear / providers. Next: per-repo VM config (VM1–VM4).

- **VM1 — done** (`~/.attractor/runs/vm-VM1`). 7 atomic commits, gate green,
  no intervention: VM launcher resolves the boot script per-run from a
  `vm_images` name→script registry; repeatable `--vm-runner name=path` (bare
  = default); per-run image carried from the submission; plus defensive
  fixes (gate VM activation on explicit intent, reject empty boot paths).
  Key commits `pxznzqyt`, `rtkmuytn`, `nqkoxluk`, `nsyymxqw`, `tlrumspn`.

- **VM2 — done** (`~/.attractor/runs/vm-VM2`). 3 atomic commits, gate green,
  no intervention: `repos.<name>` schema gains `runner` + `vm.image`;
  projected for dispatch. `sqxtknuv`, `xtxsqqzz`, `yzkplmlr`.

- **VM3 — done** (`~/.attractor/runs/vm-VM3`). 5 atomic commits, gate green:
  `resolvePlacement` applies submission > repo > default precedence for
  runner+image; VM launcher reports its registered images (`ImageValidator`)
  so unknown names are rejected; shared `resolveRepoRef`. `szllwtkw`,
  `tzmzsxsz`, `vvtzmppo`, `ukpyuwqk`, `zowkmspm`.

## Interventions

- **VM3**: the dogfood left one empty, undescribed commit (`rkqquxku`,
  0 files) in the stack — a stray checkpoint. Abandoned manually
  (`jj abandon`); descendants rebased cleanly. No code impact.
