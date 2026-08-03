# Implementation prompt — VM workspace & dependency isolation

Paste this to kick off the work. It is written to be **acceptance-driven**:
the task is done when — and only when — every check under "Definition of
done" passes. Do not mark it complete until all of them are green, with
tests committed.

---

## Task

Make attractor VM runs execute against a **per-run, isolated,
dependency-correct** copy of the target repo, so that (a) many VMs of the
same repo can run at once, (b) repo tooling that needs real filesystem
semantics (SQLite, file locks) works, and (c) a workspace's dependencies
always match its own lockfile — never another branch's.

Design and rationale: `docs/vm-workspace-spec.md`. Implement the locked
decision there: a **per-run jj workspace on the host**, **virtiofs**-mounted
(not 9p) read-write into the guest, plus a **warm per-repo pnpm store** also
virtiofs-mounted; the job dir and nix store stay 9p (read-only). The
mutable copy lives on the host so results are visible in `jj log` with no
export step. **Fallback:** if the W2 SQLite-over-virtiofs test proves
unreliable, switch to a guest-local clone with bundle export (spec's
"Alternatives"). If you deviate, say why in the commit message.

## Context you need

- The current VM launcher (`internal/server/launcher_vm.go`) 9p-mounts the
  host checkout **read-write** at `/mnt/workspace`. This is what you are
  replacing. It fails two ways, both reproducible:
  - **SQLite on 9p:** `pnpm install` in a VM dies with
    `[ERR_SQLITE_ERROR] disk I/O error` (the store's `index.db`).
  - **No isolation:** two VMs of one repo share one rw dir.
- `node_modules/` is gitignored, so a jj workspace / git worktree contains
  **no** `node_modules` — reinstalling per workspace is the *correct*
  default. The pnpm store is content-addressed and safe to share; a
  `node_modules` tree is **not** — never share or copy it across
  workspaces.
- VCS is **jj** (never git). Repo checkouts are jj-colocated.
- This host has **no `/dev/kvm`**; VMs run under TCG (`accel=kvm:tcg`).
  Slower, but correctness is fully testable — do not require KVM.
- Existing `direct` and `local` launchers must keep working unchanged.

## Definition of done (STOP when ALL are true)

Correctness (each is an automated test that must exist and pass):

1. **Isolation:** a VM run's file mutations do **not** appear in the host
   checkout, and two runs of the same repo do not see each other's
   mutations. Test: run two pipelines concurrently against the same repo,
   each writing a distinct sentinel file / commit; assert neither sentinel
   leaks to the host checkout or the other run.
2. **SQLite works over virtiofs (the bug that started this, and the pivotal
   gate):** a pipeline whose tool step runs `pnpm install` (or any SQLite
   open) **succeeds** in the virtiofs-mounted workspace — no `disk I/O
   error` — and in-guest `jj` (`diff`/`status`/`commit`) works against the
   host store through the mount. If this cannot be made reliable, invoke the
   guest-local-clone fallback rather than shipping a flaky mount.
3. **Dependency correctness:** given two branches/lockfiles pinning
   *different* versions of the same package, two concurrent runs each
   produce a `node_modules` matching **their own** lockfile. Test asserts
   the resolved version in each run's tree; a stale/cross-branch version is
   a failure.
4. **Warm-cache speed (no re-download):** the second run of a repo installs
   dependencies **without network fetches** (links from the reused
   content-addressed cache volume). Test/observe: cold vs warm install;
   warm run performs no registry downloads.
5. **Result extraction:** a commit/diff produced inside the VM is
   retrievable on the host (lands in the shared jj store or is returned as
   a diff/artifact). Test round-trips a change out of the guest.
6. **Cleanup / bounded disk:** after N runs, per-run workspaces and VM
   disks are GC'd and total disk usage is bounded (cache volume persists,
   scratch does not). Test asserts scratch is reclaimed.

Engineering gate (must hold at the tip):

7. `nix develop -c go test ./... -race` passes; `gofmt` clean; `nix build
   .#attractor` and `nix build .#vm-runner` succeed; no jj conflicts.
8. `direct` and `local` launcher runs are unaffected (their existing tests
   still pass).

## Edge cases that MUST be handled and tested

- **node_modules is untracked:** a fresh workspace has none; install
  materializes it — never inherit a copied/shared tree.
- **Lockfile change between runs:** a run on an updated lockfile installs
  the new deps (cache still helps, tree is correct).
- **Concurrent installers into one cache volume:** N VMs installing at once
  must not corrupt the shared content-addressed store (rely on pnpm's store
  concurrency, or isolate per-run and seed from a RO master — pick one and
  test it under concurrency).
- **Run crashes mid-install:** the cache volume and the next run are not
  left corrupt; a partially-written workspace is discarded.
- **Host disk pressure:** creating a workspace/volume when disk is low
  fails the run cleanly with a clear error, not a corrupt half-state.
- **Repo with no lockfile / non-pnpm** (go, pip, cargo, or nothing): the
  run still works; the deps step degrades gracefully or uses the
  generalized cache pattern (at minimum: does not crash).
- **Repo tooling that itself uses SQLite** (not just pnpm) works in the
  workspace (same guest-local guarantee).
- **Result of a *failed* run** is still extractable/inspectable (console +
  any partial diff), consistent with the existing crash-reporting fix.

## Constraints

- jj only (never git) for VCS operations in the daemon and tests.
- The mutable working copy is a **host jj workspace mounted over virtiofs**
  (so it stays host-visible); do **not** reintroduce a read-write **9p**
  workspace mount. 9p stays only for read-only shares (job dir, nix store).
- Do not require KVM. Tests must pass under TCG (the real box has KVM; KVM
  changes speed only, not the virtiofs wiring).
- Small, atomic, reviewable commits; follow `docs/vm-workspace-spec.md`'s
  milestone ledger and flip each Status to `done` in its final commit.
- If you build via the self-dev pipeline, run it milestone-by-milestone
  with the review-core fanout, as in `docs/build-goal-log.md`.

## Out of scope (do not do these)

- KVM enablement / VM performance tuning.
- The guest-local-clone path — build it *only* if W2 shows virtiofs locking
  is unreliable; otherwise virtiofs is the chosen delivery.
- Distributed / cross-host placement.
- UI changes.

## Deliverable

All milestones W1–W5 in `docs/vm-workspace-spec.md` marked `done`, every
"Definition of done" check backed by a committed passing test, the gate
green, and a short note in `docs/build-goal-log.md` recording what was
built and any design deviations. W6 (non-node caches) may be deferred but
must be explicitly noted as such.
