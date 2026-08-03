# VM workspace & dependency isolation spec

How a pipeline run gets an **isolated, dependency-correct** copy of a repo
inside its VM — so we can run **many VMs of the same repo at once** (jj
workspaces / git worktrees), with tooling that needs real filesystem
semantics (SQLite, file locks) working, and **no stale-dependency bugs**.

Status: **design proposal for grilling.** No code yet. Supersedes the
current VM launcher's approach of read-write-mounting the host checkout.

## The problem (why today's design is wrong)

A VM run today 9p-mounts the real host checkout **read-write** at
`/mnt/workspace`. Two independent failures:

1. **No isolation.** Two VMs of one repo mount the same directory and
   clobber each other. Concurrent same-repo runs are impossible.
2. **Broken filesystem semantics.** 9p can't provide the locking/mmap
   SQLite needs. Proven live: `pnpm install` in a VM dies with
   `[ERR_SQLITE_ERROR] disk I/O error` opening its store index
   (`index.db`). Ghost's own SQLite-backed tests would fail the same way.

The 9p-vs-virtiofs question is a red herring on its own — even a perfect
share doesn't give isolation, and a *read-write* share of one dir to N VMs
is unsafe regardless of protocol.

## Requirements (the bar the solution must clear)

1. **Isolation** — a run's mutations never touch the host checkout or
   another run's copy.
2. **Concurrency** — N VMs of the *same* repo run simultaneously, each
   correct and independent.
3. **Filesystem semantics** — SQLite / `flock` / `mmap` work in the
   workspace (pnpm store, app test DBs).
4. **Dependency correctness** — `node_modules` always matches *that
   workspace's* lockfile; a different branch never inherits another's deps.
5. **Speed** — no full dependency re-download per run; warm cache reuse.
6. **Result extraction** — commits / diffs / artifacts flow back to the
   host.
7. **Cleanup** — workspaces, VMs, and cache volumes are GC'd; disk stays
   bounded.

## Two-axis reframe

Every option is a pair of choices:

- **Materialize** — how the isolated per-run copy is made.
- **Deliver** — how the guest gets at it.

`9p` vs `virtiofs` is only the *Deliver* axis. Solve *Materialize* first.

## The guiding principle: the mutable copy lives on guest-local disk

Put the **mutable working copy on the guest's own disk** (its qcow2 ext4).
Then:

- SQLite / locks / mmap just work (native fs).
- Isolation is free (each VM has its own disk).
- 9p-vs-virtiofs stops being a *correctness* question; a share, if kept,
  is only a read-only *transport* for source or a warm cache.

A *shared, read-write* workspace is the one design that drags virtiofs in
as a correctness requirement — so we avoid it.

## Dependencies — the heart of it

This is where the "stale node_modules across branches" bug lives, so it
gets its own treatment.

**Key fact:** jj workspaces / git worktrees materialize **only tracked
files**. `node_modules/` is gitignored → untracked → a fresh workspace has
**none**. So the default is: *fresh workspace, no deps, reinstall.* That is
the **correct** behaviour — there is no shared mutable `node_modules`, so a
branch can never run against another branch's dependencies.

**The bug only appears if you copy or share `node_modules`.** So the rule
is: **never share or copy `node_modules` across workspaces.** Materialize
it per-workspace, always, from that workspace's lockfile.

**Making reinstall fast — share the store, not the tree.** pnpm's store is
**content-addressed** (`files/`, `links/`, keyed by content hash) — dep v1
and v2 are different entries that never collide, so it is safe to share and
reuse. `pnpm install` against a warm store just *links*; no download. So:

- Share/reuse the **content-addressed store** → fast.
- Materialize **`node_modules` per workspace** → correct.

**The store's catch:** its index is **SQLite** (`index.db`). A shared store
must therefore live on a real filesystem (a **guest-local reused volume**,
or virtiofs) — never 9p — and concurrent installers writing one store need
a strategy (below).

## Options matrix — Materialize

| Approach | Isolated | Guest-local mutable | Deps | Cost |
|---|---|---|---|---|
| Mount real checkout rw (today) | ❌ | ❌ (9p) | shared (unsafe) | breaks SQLite + concurrency |
| Fresh clone from remote in guest | ✅ | ✅ | reinstall | slow, network, full history |
| jj workspace / local clone → deliver to guest disk | ✅ | ✅ | reinstall (warm store) | cheap source, native fs |
| Copy checkout incl `node_modules` | ✅ | ✅ | copied (⚠ stale risk if reused) | 1.7 GB/run |
| reflink/CoW per-run copy on host | ✅ | ❌ (needs share) | ~free | needs reflink fs + virtiofs |
| Overlayfs: RO base + per-run upper | ✅ | partial | shared base | complex |

## Options matrix — Dependency delivery

| Approach | Correct | Fast | fs-safe |
|---|---|---|---|
| Warm per-repo store on a reused **guest-local volume** + per-run `install` | ✅ | ✅ | ✅ (native) | **recommended** |
| Bake deps into the image | ⚠ stale-prone | ✅ | ✅ | repo-specific |
| Shared RO store (virtiofs) + per-run install | ✅ | ✅ | ✅ | needs virtiofs |
| Reinstall from network each run | ✅ | ❌ | ✅ | simple, slow |

## Recommendation

**Per-run jj workspace on the host → its tracked files delivered to
guest-local disk → per-run `install` into a fresh `node_modules`, linking
from a warm per-repo content-addressed store on a reused guest-local
volume.**

Delivers isolation, concurrency, native filesystem, dependency
correctness, and speed. virtiofs becomes an optional transport speed-up,
not a correctness dependency. jj gives cheap host-side isolation and clean
result extraction (a run's commit/diff lands in the shared store).

## Open decisions (for the grill)

1. **Deliver mechanism** — copy tracked files in, `git/jj` local-clone
   onto guest disk, or RO-share the source + check out in-guest?
2. **Store under concurrency** — one per-repo store volume shared rw across
   N VMs (needs locking / pnpm's own concurrency guarantees verified), vs a
   per-run store seeded from a RO master, vs copy-on-write volumes?
3. **Result commit path** — commit in-guest via jj (needs store access) vs
   host-side from the returned diff?
4. **Non-node repos** — generalize "installer + reused cache volume" to
   `go` module cache, pip wheel cache, cargo registry, etc.?
5. **Volume lifecycle** — per-repo cache volume creation, GC, and a disk
   cap; behaviour on host disk pressure.
6. **jj store access in-guest** — do we ship the `.jj`/git object store (so
   in-guest jj works), or only the working files?

## Milestones (draft ledger)

| # | Deliverable | Status |
|---|---|---|
| W1 | Retire the rw 9p workspace mount; a run gets a **per-run isolated working copy** (materialized on host, mutable copy on guest-local disk) | todo |
| W2 | **Acceptance test passes:** `pnpm install` (SQLite store) succeeds in a VM against the isolated copy — the exact case that fails today | todo |
| W3 | Warm **per-repo store volume** reused across runs; installs link (no re-download); a **dependency-correctness test** proves a fresh `node_modules` matches the workspace lockfile | todo |
| W4 | **Concurrency:** N VMs of the same repo run at once, isolated + correct (no cross-contamination, no store corruption) | todo |
| W5 | **Result extraction** (commit/diff/artifacts back to host) + **cleanup/GC** of workspaces, VMs, volumes with a disk cap | todo |
| W6 | Generalize the deps pattern beyond pnpm (go/pip/cargo cache volumes) | todo |

## Non-goals (v1)

- KVM / performance tuning (TCG is fine for correctness work).
- Editing the runtime image contents from the UI.
- Cross-host / distributed VM placement.

See `docs/vm-workspace-prompt.md` for the acceptance-driven implementation
prompt (the executable "definition of done").
