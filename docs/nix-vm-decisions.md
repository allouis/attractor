# Nix VM runners — decision log

Append-only. Each entry: context, options, decision, why. Hard/50-50 calls are
debated by subagents (recorded here).

## D1 — Accel: TCG fallback, KVM when available
**Context:** Dev host is a KVM guest with no nested virt (CPU exposes only
`hypervisor`; `modprobe kvm_intel/amd` → "Operation not supported"). No nix
config adds a CPU feature the host hypervisor doesn't pass through.
**Decision:** VM launcher auto-detects `/dev/kvm`: `-accel kvm` if present,
else `-accel tcg`. Never hard-require KVM.
**Why:** Correctness/dogfooding don't need speed; TCG works. Auto-detect keeps
it portable to KVM-capable hosts with zero config change.

## D2 — Isolation unit = the run (one VM per run)
**Context:** Per-node isolation would force workspace sync between environments.
**Decision:** One VM per run; nodes share the VM's working tree.
**Why:** Nodes hand off work through the repo, not just Context. The repo is the
coupling; the run is the natural isolation boundary.

## D3 — Comms: HTTP phone-home everywhere (not shared-FS)
**Context:** Shared FS could carry state for local/container but breaks down for
remote (network FS across a real boundary is flaky/heavy) and needs two impls.
**Decision:** Single transport — child POSTs events/artifacts, polls control —
for local-subprocess, VM, and remote.
**Why:** One code path. Loopback HTTP is fast/reliable locally; same code works
remote. Reuses the existing `internal/ingest` phone-home pattern. Bonus: crash
isolation (a run panic no longer kills the daemon).

## D4 — Phone-home human gates deferred; report-to defaults to AutoApprove
**Context:** The `interview_started` event carries no options, so a polling
child can't reconstruct the choice set without enriching the event schema.
VM/CI runs are non-interactive by nature.
**Decision:** `attractor run --report-to` uses a non-interactive interviewer
(AutoApprove) for now. Cancel still works via `GET /control` (D3). Poll-based
human gates (enrich `interview_started` with options; child polls `/control`
for the answer) are a Phase-4 milestone.
**Why:** Unblocks the VM dogfooding goal fastest; human gates inside automated
VM runs are uncommon; nothing here precludes adding the poll path later.

## D5 — Keep in-process as the `direct` Launcher; add `local`/`vm` alongside
**Context:** Two subagents debated retiring in-process execution entirely (all
runs → subprocess phone-home) vs keeping it. Grounded findings:
- Only 4 integration tests actually execute runs to completion in-process
  (router_dispatch, run_action ×2, itemref) — all simulation, no binary.
- A subprocess launcher needs the `attractor` binary built + on PATH and
  provider-config discovery, neither of which `go test` provides — so
  retiring now forces a TestMain-builds-binary or fake-launcher rewrite.
- Both sides agree a `Launcher` seam is right, and that `deliver()` already
  converges in-process fan-out and phone-home Ingest.
**Decision:** Introduce a `Launcher` seam; route `submit`→dispatcher through it.
Keep the in-process path as the `direct` launcher, which stays the **default**
so every existing test stays green. Add `local` (subprocess) and `vm`
launchers as opt-in. Do NOT retire `direct` yet — retire only once `local`+`vm`
are proven, when it's a cheap deletion.
**Why:** Captures the structural win (one seam; local and vm slot in cleanly)
without paying the blast-radius cost now. Phone-home runs self-complete from
the ingested terminal event so `direct` and `local`/`vm` share completion.
