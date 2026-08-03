# VM workspace & dependency isolation spec

How a pipeline run gets an **isolated, dependency-correct, host-visible**
copy of a repo inside its VM — so we can run **many VMs of the same repo at
once**, with tooling that needs real filesystem semantics (SQLite, file
locks) working, **no stale-dependency bugs**, and the resulting code
**immediately visible on the host**.

Status: **design settled via a grilling session** (decision record below).
No code yet. Supersedes the current VM launcher's read-write 9p mount of
the host checkout.

## The problem (why today's design is wrong)

A VM run today 9p-mounts the real host checkout **read-write** at
`/mnt/workspace`. Two independent failures:

1. **No isolation.** Two VMs of one repo mount the same directory and
   clobber each other. Concurrent same-repo runs are impossible.
2. **Broken filesystem semantics.** 9p can't provide the locking/mmap
   SQLite needs. Proven live: `pnpm install` in a VM dies with
   `[ERR_SQLITE_ERROR] disk I/O error` opening its store index
   (`index.db`). Ghost's own SQLite-backed tests would fail the same way.

The `9p`-vs-`virtiofs` question is not the whole story: even a perfect
share doesn't give isolation, and a *read-write* share of one directory to
N VMs is unsafe regardless of protocol. So we need **a per-run copy**, on a
filesystem with **real POSIX semantics**, that stays **visible on the
host**.

## Requirements

1. **Isolation** — a run's mutations never touch the host checkout or
   another run's copy.
2. **Concurrency** — N VMs of the *same* repo run simultaneously, each
   correct and independent.
3. **Filesystem semantics** — SQLite / `flock` / `mmap` work in the
   workspace (pnpm store, app test DBs).
4. **Dependency correctness** — `node_modules` always matches *that
   workspace's* lockfile; a different branch never inherits another's deps.
5. **Speed** — no full dependency re-download per run; warm cache reuse.
6. **Host visibility / result extraction** — the resulting code and commits
   are directly visible on the host, no manual export step.
7. **Cleanup** — per-run workspaces and VMs are GC'd; the per-repo cache
   persists; disk stays bounded.

## Decision record (locked)

| Topic | Decision |
|---|---|
| **Working copy** | A **per-run jj workspace on the host** (`jj workspace add`), materialising an isolated working copy that lives on the host and appears in `jj log`. jj workspaces are the host-side isolation primitive; the target repos are jj-colocated. |
| **Delivery to guest** | **virtiofs**, not 9p. Mount the per-run host workspace read-write into the guest. virtiofs has the locking/mmap semantics SQLite and jj need; 9p does not. |
| **Why not guest-local disk** | A guest-local clone also gives correct semantics, but the code would be trapped in the VM behind an export step. virtiofs keeps the mutable copy **on the host** → instantly visible (`jj log`, `cd` in) → requirement 6. (Guest-local clone is the **fallback** if virtiofs locking proves unreliable — see W2.) |
| **In-guest VCS** | Pipelines run `jj` **inside the run** (23 call sites: `jj diff`, `jj show @-`, `jj status`, `jj squash`, `jj resolve --list`, review `jj diff --from base --to @`). virtiofs-mounting the host workspace gives the guest a working jj repo — its `jj` operations write to the host store through the mount. This is a hard requirement the design must satisfy, and a reason the mount must be virtiofs (jj's working-copy state + locking need real semantics). |
| **Dependencies** | **Share the store, never `node_modules`.** `node_modules/` is gitignored → a fresh workspace has none → install per run (correct: no cross-branch staleness). Make install fast with a **warm per-repo pnpm store** (content-addressed, hash-keyed, safe to share) **virtiofs-mounted from the host**, shared read-write across concurrent runs; installs *link* from it (no re-download). |
| **Store concurrency** | Rely on **pnpm's built-in store locking + virtiofs locking** for N concurrent installers into one per-repo store. Same locking guarantee the SQLite acceptance test already proves. Fallback: per-run store seeded copy-on-write from a read-only master, only if concurrency proves unsafe. |
| **Keep 9p where it's fine** | Undemanding **read-only** shares stay 9p (module-blessed, daemon-free): the job dir (`job.json` + `source.dot`) and, optionally, the host `/nix/store` (the nixpkgs qemu-vm module supports a 9p RO nix-store mount). virtiofs is used **only** for the workspace + the pnpm store — the two that need locking. |

## How it works (the flow)

For a VM run against repo `R`:

1. **Materialise (host):** `jj workspace add <runs>/<id>/work` — an isolated
   working copy of `R`, on the host, in `jj log`.
2. **Deliver (virtiofs):** launcher starts a per-run `virtiofsd` for that
   workspace dir and wires the VM with `-device vhost-user-fs` + a
   shared-memory backend; the guest mounts it rw at `/mnt/workspace`.
3. **Warm deps (virtiofs):** a per-repo store dir
   (`<cache>/<repo>/pnpm-store`) is virtiofs-mounted rw into the guest;
   pnpm's `store-dir` points at it, so `install` links from the warm store.
4. **Light shares (9p):** the job dir (ro) and optional `/nix/store` (ro)
   stay 9p.
5. **Run:** the guest installs deps (fresh `node_modules` in the workspace,
   linked from the warm store), builds/tests (SQLite works over virtiofs),
   and runs `jj` (writes to the host store over virtiofs).
6. **Result:** the workspace and its commits are **already on the host** —
   visible via `jj log`; no export. The reaper GCs the per-run workspace +
   VM after retention; the per-repo store cache persists.

**Concurrency:** each run gets its own workspace dir + its own virtiofs
mount → isolated. The shared per-repo store is safe via pnpm + virtiofs
locking.

## Dependencies — the heart of it (answers the node_modules concern)

- jj workspaces materialise **only tracked files**. `node_modules/` is
  gitignored → a fresh workspace has **none** → install per run. This is
  the *correct* default: there is no shared mutable `node_modules`, so a
  branch can never run against another branch's dependencies.
- **The stale-deps bug only appears if you copy or share `node_modules`.**
  Rule: **never** share or copy `node_modules` across workspaces.
- Fast install comes from sharing the **content-addressed store** (`files/`,
  `links/`, keyed by content hash — v1 and v2 of a dep are distinct
  entries, never collide), not the linked tree. `pnpm install` against a
  warm store just links.
- The store's index is **SQLite** (`index.db`) — which is exactly why the
  store share must be **virtiofs**, and why the whole approach hinges on
  virtiofs locking being solid.

## Why virtiofs, not 9p (context)

9p is the NixOS qemu-vm module's default because it is **older** (in QEMU
since ~2010 vs virtiofs' 2019), **daemon-free** (lives inside QEMU; one
flag), and **adequate for that module's job** (light-IO integration tests,
read-only nix store). It was the low-friction MVP choice here too (decision
D6: `virtualisation.sharedDirectories` is the blessed, tested path and is
**9p-only** in our nixpkgs). Its ceiling — no SQLite/locking — only bites a
real dev repo, which is where we now are.

virtiofs is a semantic superset (flock, byte-range locks, mmap, coherent
metadata) and faster, at the cost of a per-share **`virtiofsd`** daemon + a
**shared-memory** VM config + hand-rolled qemu args (the nixpkgs module
won't do it for us). That complexity is the price of correctness + host
visibility.

**KVM note:** the production target is bare-metal **KVM**. KVM is only the
CPU accelerator — you still run **QEMU** (`-machine accel=kvm`), and KVM
does **not** simplify virtiofs setup (same `virtiofsd` + vhost-user +
shared-mem). What KVM changes: it removes the TCG speed penalty, so both
this design and any fallback run at near-native speed on the real box.

## Alternatives considered (rejected / fallback)

| Approach | Verdict |
|---|---|
| Mount real checkout rw over 9p (today) | ✗ breaks SQLite; no isolation |
| Guest-local clone + bundle export | fallback if virtiofs locking fails; costs an export step (code not host-visible) |
| Copy checkout incl `node_modules` | ✗ 1.7 GB/run + stale-deps risk |
| reflink/CoW host copy + virtiofs | possible optimisation of the workspace copy; not needed for v1 |
| Bake deps into image | ✗ stale-prone, repo-specific |

## Open decisions (still to resolve during build)

1. **virtiofsd cache mode** (`none`/`auto`/`always`, DAX) for host↔guest
   coherence + locking — a tuning detail the W2 test pins down.
2. **Non-node deps** — generalise "installer + warm cache share" to `go`
   module cache, pip wheel cache, cargo registry (W6).
3. **Cache GC + disk cap** — per-repo store growth bound and eviction (W5).
4. **jj workspace lifecycle** — `jj workspace forget`/cleanup after a run,
   and behaviour of concurrent workspace ops against `R`'s store.

## Milestones

| # | Deliverable | Status |
|---|---|---|
| W1 | **virtiofs plumbing:** per-run `virtiofsd` + qemu `vhost-user-fs` + shared-memory; mount a per-run **host jj workspace** rw at `/mnt/workspace`, replacing the 9p rw mount. Job dir + nix store stay 9p (ro). | todo |
| W2 | **Pivotal acceptance test:** `pnpm install` (SQLite store) **succeeds** in the virtiofs-mounted workspace, and in-guest `jj` (`diff`/`status`/`commit`) works against the host store. Decides virtiofs viability; if it fails, switch to the guest-local-clone fallback. | todo |
| W3 | **Warm per-repo pnpm store** virtiofs-mounted (shared rw); installs link (no re-download); **dependency-correctness test** — two branches/lockfiles pinning different versions → each `node_modules` matches its own. | todo |
| W4 | **Concurrency:** N VMs of the same repo at once — own workspace + own mount each; shared store uncorrupted; isolation + correctness tests. | todo |
| W5 | **Lifecycle:** reaper GCs per-run workspace + VM; per-repo store persists with a **disk cap**; result visible on host (`jj log`) with no export step. | todo |
| W6 | Generalise the deps pattern beyond pnpm (go/pip/cargo cache shares). | todo |

## Non-goals (v1)

- KVM enablement / perf tuning (correctness is testable under TCG; the real
  box has KVM).
- Editing the runtime image from the UI.
- Cross-host / distributed VM placement.

The acceptance-driven implementation prompt (executable "definition of
done") is `docs/vm-workspace-prompt.md`.
