# Build-goal log

Running log for the "build out attractor, using attractor" goal
(2026-08). Deliverables: build the **config screen**, then spec + build
**per-repo VM config** and a **team-ready web UI**. Built by dogfooding
the `build-review` pipeline (plan → implement → verify → 5-lens review-core
loop → record). Small atomic commits; every intervention recorded here.

## Plan

| Spec | Status | Build |
|---|---|---|
| `config-screen-spec.md` | decisions locked | C1–C5 todo |
| `repo-vm-config-spec.md` | drafted | VM1–VM4 todo (after config-screen C1) |
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

## Interventions

_(none yet — infra fixes, graph edits, manual unblocks recorded here as
they happen)_
