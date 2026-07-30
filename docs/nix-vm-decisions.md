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
