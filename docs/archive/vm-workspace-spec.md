# VM workspace & dependency isolation spec

How a pipeline run gets an **isolated, dependency-correct, host-visible**
copy of a repo inside its VM — so we can run **many VMs of the same repo at
once**, with tooling that needs real filesystem semantics (SQLite, file
locks) working, **no stale-dependency bugs**, and the resulting code
**immediately visible on the host**.

Status: **W1–W2 shipped virtiofs delivery; then Ghost empirically disproved
it** — virtiofsd `cache=none` cannot host SQLite for real tools (pnpm store
*and* Nx DB both `disk I/O error`). **Current approach is guest-local
(G1–G4) — see the "Empirical pivot" section at the end; that is the source
of truth.** The virtiofs decision record below is retained as history. G1 (guest-local
copy) is **done**; the next `todo` milestone is **GS** (remove the now-dead
virtiofs machinery, transport via read-only 9p).

## The problem (why today's design is wrong)

A VM run today 9p-mounts the real host checkout **read-write** at
`/mnt/workspace`. Two independent failures:

1. **No isolation.** Two VMs of one repo mount the same directory and
   clobber each other. Concurrent same-repo runs are impossible.
2. **Broken filesystem semantics.** 9p can't provide the locking/mmap
   SQLite needs. Proven live: `pnpm install` in a VM dies with
   `[ERR_SQLITE_ERROR] disk I/O error` opening its store index
   (`index.db`). Ghost's own SQLite-backed tests would fail the same way.

Even a perfect share doesn't give isolation, and a *read-write* share of
one directory to N VMs is unsafe regardless of protocol. So we need **a
per-run copy**, on a filesystem with **real POSIX semantics**, that stays
**visible on the host**.

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
7. **Cleanup** — per-run workspaces and VMs are GC'd; the global cache
   persists; disk stays bounded (see the store-GC decision below).

## Decision record (locked via grilling)

| Topic | Decision |
|---|---|
| **Working copy** | A **per-run jj workspace on the host** (`jj workspace add`) — isolated working copy that lives on the host and appears in `jj log`. jj workspaces are the host-side isolation primitive; target repos are jj-colocated. |
| **Delivery to guest** | **virtiofs**, not 9p — rw mount of the per-run host workspace. virtiofs has the locking/mmap semantics SQLite and jj need; 9p does not. |
| **Why not guest-local disk** | A guest-local clone gives correct semantics too, but the code would be trapped behind an export step. virtiofs keeps the mutable copy **on the host** → instantly visible (`jj log`, `cd` in) → requirement 6. Guest-local clone is the **fallback** if virtiofs locking proves unreliable (W2). |
| **In-guest VCS** | Pipelines run `jj` **inside the run** (23 call sites). The virtiofs-mounted host workspace gives the guest a working jj repo — its `jj` ops write to the host store through the mount. Hard requirement; another reason the mount must be virtiofs. |
| **Deps — what's shared** | **Share the content-addressed store, never `node_modules`.** `node_modules/` is gitignored → a fresh workspace has none → install per run (correct: no cross-branch staleness). |
| **Store scope** | **One global store per ecosystem** (pnpm's default model — content-addressed, hash-keyed, safe across all repos/branches), virtiofs-mounted into every run, shared read-write. `node_modules` stays per-workspace; only the *store* is shared. Best hit-rate, least disk. |
| **Store concurrency** | Rely on **pnpm's store locking + virtiofs locking** for N concurrent installers into the one global store — the same guarantee W2 proves for SQLite. Fallback: per-run store seeded copy-on-write from a read-only master, only if concurrency proves unsafe. |
| **Cache mechanism** | A **generic cache-mount seam** from day one: the launcher mounts a configured list of `{host dir → guest path → env var}` globally into every run. The pnpm store is the first entry; go (`GOMODCACHE`/`GOCACHE`), cargo (`CARGO_HOME`), pip (`PIP_CACHE_DIR`) are added later. Config-extensible; no pnpm special-casing. |
| **9p kept** | Undemanding **read-only** shares stay 9p (module-blessed, daemon-free): the job dir (`job.json` + `source.dot`) and optionally the host `/nix/store`. virtiofs is used **only** for the workspace + the cache mounts. |
| **Result surfacing** | The run **bookmarks its tip** (`run/<id>` or the item identifier). Since in-guest `jj` commits into R's shared store, the bookmarked commit shows in R's `jj log` and is checkout-able (`jj new run/<id>`) — the handle on the result. |
| **Workspace lifecycle** | **Reclaim on success; keep failed runs until the retention window.** A crashed run's *uncommitted* working-copy state is evidence worth inspecting; success leaves nothing but the (bookmarked, committed) tip, which lives in R's store independent of the workspace. Cleanup is `jj workspace forget` + rm on the existing VM-reaper timer. |
| **Store GC** | **Grow freely, prune manually.** No automatic eviction; document the native prune commands (`pnpm store prune`, etc.) and optionally surface store size for visibility. Pragmatic on a bare-metal box with real disk. |
| **virtiofsd cache mode** | Default **`none`** (strongest coherence + locking — needed for SQLite and instant host visibility). W2 may relax to `auto` if it's slow and the lock test still passes. `always` is ruled out (stale cross-boundary reads). |

## How it works (the flow)

For a VM run against repo `R`:

1. **Materialise (host):** `jj workspace add <runs>/<id>/work` — an isolated
   working copy of `R`, on the host, in `jj log`.
2. **Deliver (virtiofs):** launcher starts a per-run `virtiofsd`
   (`cache=none`) for that workspace and wires the VM with `-device
   vhost-user-fs` + a shared-memory backend; the guest mounts it rw at
   `/mnt/workspace`.
3. **Warm caches (virtiofs):** the generic cache seam mounts each global
   ecosystem cache (v1: the pnpm store) rw into the guest and sets its env
   var (v1: pnpm `store-dir`), so `install` links from the warm store.
4. **Light shares (9p):** the job dir (ro) and optional `/nix/store` (ro)
   stay 9p.
5. **Run:** the guest installs deps (fresh `node_modules` in the workspace,
   linked from the warm store), builds/tests (SQLite works over virtiofs),
   runs `jj` (writes to the host store over virtiofs), and **bookmarks its
   tip** `run/<id>`.
6. **Result:** the workspace and its commits are **already on the host** —
   `jj log` shows the bookmarked tip; `jj new run/<id>` to work from it. On
   **success** the reaper reclaims the workspace (`jj workspace forget` +
   rm) promptly; on **failure** it's kept until retention for inspection.
   The global caches persist.

**Concurrency:** each run gets its own workspace dir + its own virtiofs
mount → isolated. The shared global store is safe via pnpm + virtiofs
locking. `jj workspace add`/`forget` mutate R's op log; jj is built for
concurrent workspace ops (op-log merging) — a spot to watch under load
(W4).

**Operator notes (delivery, from the W2 build):**

- **What crosses into the guest, rw.** Only the per-run workspace
  (`/mnt/workspace`) and R's jj store — its `.jj` + colocated `.git` at
  `/mnt/repo/.jj` + `/mnt/repo/.git`. The repo **root is not** mounted, so
  R's working tree (source, `.env`/secrets, `node_modules`) never reaches
  the guest. The store is mounted **read-write** (jj must commit), so a
  pipeline can write R's shared store — the same trust boundary as the
  shared pnpm store; keep untrusted code out of runs against a repo whose
  store you care about.
- **Same-repo concurrency is unproven until W4.** N concurrent runs share
  one jj store over virtiofs; safe concurrent multi-writer access is a W4
  deliverable. Until then the safe operating assumption is **one live run
  per repo**.
- **Guest-oriented workspace pointer.** A run's workspace `.jj/repo` is
  rewritten to the guest mount path while it runs; the launcher restores the
  host-valid pointer on any non-success exit, so a failed run kept for
  inspection stays host-`jj`-usable. Inspect results via `jj -R <repo>`
  (where the run's commits live), not by `cd`-ing into a live run's
  workspace.

## Dependencies — the heart of it (answers the node_modules concern)

- jj workspaces materialise **only tracked files**. `node_modules/` is
  gitignored → a fresh workspace has **none** → install per run. This is
  the *correct* default: no shared mutable `node_modules`, so a branch can
  never run against another branch's dependencies.
- **The stale-deps bug only appears if you copy or share `node_modules`.**
  Rule: **never** share or copy `node_modules` across workspaces.
- Fast install comes from sharing the **content-addressed store** (keyed by
  content hash — v1 and v2 of a dep are distinct entries, never collide),
  not the linked tree. One global store per ecosystem; `pnpm install`
  against it just links.
- The store's index is **SQLite** (`index.db`) — which is why the store
  mount must be **virtiofs**, and why the whole approach hinges on virtiofs
  locking (W2).

## Why virtiofs, not 9p (context)

9p is the NixOS qemu-vm module's default because it is **older** (QEMU
~2010 vs virtiofs 2019), **daemon-free** (inside QEMU; one flag), and
**adequate for that module's job** (light-IO integration tests, ro nix
store). It was the low-friction MVP choice here too (decision D6:
`virtualisation.sharedDirectories` is the blessed, tested path and is
**9p-only** in our nixpkgs). Its ceiling — no SQLite/locking — only bites a
real dev repo.

virtiofs is a semantic superset (flock, byte-range locks, mmap, coherent
metadata) and faster, at the cost of a per-share **`virtiofsd`** daemon + a
**shared-memory** VM config + hand-rolled qemu args (the nixpkgs module
won't do it for us). That complexity is the price of correctness + host
visibility.

**KVM note:** the production target is bare-metal **KVM**. KVM is only the
CPU accelerator — you still run **QEMU** (`-machine accel=kvm`), and KVM
does **not** simplify virtiofs setup (same `virtiofsd` + vhost-user +
shared-mem). It removes the TCG speed penalty, so this design (and any
fallback) run near-native on the real box.

## Alternatives considered (rejected / fallback)

| Approach | Verdict |
|---|---|
| Mount real checkout rw over 9p (today) | ✗ breaks SQLite; no isolation |
| Guest-local clone + bundle export | fallback if virtiofs locking fails; costs an export step (code not host-visible) |
| Copy checkout incl `node_modules` | ✗ 1.7 GB/run + stale-deps risk |
| Per-repo (not global) store | ✗ duplicates shared deps, no benefit (content-addressed) |
| Automatic store eviction / TTL GC | ✗ over-engineered; manual prune chosen |
| virtiofsd `cache=always` | ✗ stale cross-boundary reads, weak locking |
| Bake deps into image | ✗ stale-prone, repo-specific |

## Milestones

| # | Deliverable | Status |
|---|---|---|
| W1 | **virtiofs plumbing:** per-run `virtiofsd` (`cache=none`) + qemu `vhost-user-fs` + shared-memory; mount a per-run **host jj workspace** rw at `/mnt/workspace`, replacing the 9p rw mount. Job dir + nix store stay 9p (ro). | done |
| W2 | **Pivotal acceptance test:** `pnpm install` (SQLite store) **succeeds** in the virtiofs-mounted workspace, and in-guest `jj` (`diff`/`status`/`commit`) works against the host store. Pins the cache mode (start `none`). If it fails, switch to the guest-local-clone fallback. | done |
| W3 | ~~Generic cache-mount seam, virtiofs-mounted global store~~ | **superseded** — virtiofs can't host SQLite (see §Empirical pivot); replaced by G1–G4 |
| W4 | ~~Concurrency via shared virtiofs store~~ | **superseded** — per-run VMs are already isolated under guest-local (G-milestones) |
| W5 | **Lifecycle & surfacing** — folded into G2/G4 (results via `jj bundle`; reaper reclaims guest disk) | superseded by G2/G4 |
| W6 | go/cargo/pip caches — folded into G3's cache seam | superseded by G3 |

## Open (empirical, resolved during build)

- Exact virtiofsd tuning (`none`→`auto` relaxation, DAX) — W2.
- jj op-log behaviour under heavy concurrent workspace add/forget — W4.

## Non-goals (v1)

- KVM enablement / perf tuning (correctness testable under TCG; the real
  box has KVM).
- Editing the runtime image from the UI.
- Cross-host / distributed VM placement.
- Automatic cache eviction (manual prune by decision).

The acceptance-driven implementation prompt (executable "definition of
done") is `docs/vm-workspace-prompt.md`.

## Empirical pivot: guest-local workspace (supersedes the virtiofs *work surface*)

**Finding (Ghost, real workload).** virtiofsd `cache=none` cannot host SQLite
for real tools. Two independent failures on the mount, both `disk I/O error`:
- pnpm's store index (`[ERR_SQLITE_ERROR]`), default store `.pnpm-store` on the
  workspace;
- Nx's task DB (`SqliteFailure(SystemIoFailure, 5386)`), on the workspace.

The W2 `is-number` fixture passed only because it never stressed SQLite
locking/mmap. Relocating each tool's DB by env var is unbounded whack-a-mole
(pnpm store-dir, NX_CACHE_DIRECTORY, and the next tool, and the next).

**Proven fix.** Copy the delivered workspace onto the guest's own ext4
(`/dev/vda`) and run all tools there. Ghost `pnpm install` **and** `pnpm run
lint` (Nx, 39 projects) then both succeed, ~60s, no `disk I/O error`.

**Decision.** The *work surface* is guest-local ext4, not the shared mount.
The host mount is demoted to a read-only **transport**. Two concurrent
same-repo runs already get separate VMs → separate ext4 → separate stores, so
isolation is free (no shared-store corruption to engineer — W4 dissolves).

### Milestones (supersede virtiofs W3–W6 delivery)
| # | Deliverable | Status |
|---|---|---|
| G1 | Launcher/vm-runner materialize the per-run workspace onto guest ext4 (`/work`); pipeline `cwd=/work`; the ro host mount is transport only. Acceptance (ungated host test may stub; gated e2e boots a VM): Ghost `pnpm install` + `pnpm run lint` succeed — no `disk I/O error`. | done |
| GS | **Drop virtiofs (copy-only makes it dead weight).** With guest-local copy (G1) the workspace needs only read-only *delivery*. Replace the virtiofs delivery with plain **9p** shares (module-blessed, daemon-free): remove the per-run `virtiofsd` startup, the `vhost-user-fs`/`QEMU_OPTS` device wiring, and the virtiofs `virtualisation.fileSystems` mounts. Deliver `/mnt/workspace` as **read-only** 9p (transport; the guest copies to ext4 as in G1). The repo store `/mnt/repo/.jj`+`.git` is delivered **read-write** 9p, *not* ro — the original "ro" plan was wrong here: in-guest jj auto-snapshots and commits on nearly every command, so its store mount must be writable. This does **not** re-open the pivot's failure mode: unlike the SQLite tools (pnpm/Nx) that forced the ext4 copy, jj's store is plain files (op-store, op-heads, git objects, not SQLite), and the gated `ATTRACTOR_VM_E2E` **proves** in-guest jj commits land in the host store over plain 9p. Delete the now-dead virtiofs code paths + their unit tests. Gate stays green and the gated `ATTRACTOR_VM_E2E` acceptance still passes (Ghost `pnpm install` + `pnpm run lint`, **plus** an in-guest jj commit visible in the host `jj log`). Requires jj-**colocated** repos (`jj root` + a root `.git`): the launcher rejects an external-git-backend repo up front, since the module 9p-shares `$ATTRACTOR_REPO/.git`. Net diff: virtiofs gone. | done |
| G2 | Results export: guest `jj bundle` (or `jj git push` over the ro-mounted `.git`) of the run tip → job share; host imports as `run/<id>`, visible in host `jj log`, no manual export. | deferred (user, later) |
| G3 | Warm cache: a host-persisted ext4 cache dir mounted into the guest (NOT virtiofs for SQLite dirs) or a VM-local reused store, so run N+1 skips re-download; dependency-correctness test (two lockfiles → correct node_modules each). | deferred |
| G4 | Reaper/lifecycle unchanged from W5 intent: reclaim guest disk on success, keep failed until retention. | deferred |

### Keep from W1/W2
The mount plumbing (per-run host jj workspace, 9p shares, job share,
phone-home) stays — it is the transport. Only the *work surface* moves to ext4.

### GS notes (empirical corrections + rollout)
- **jj *does* work over 9p (corrects the historical decision record).** The
  locked record above (§Decision record: "9p can't provide the locking/mmap
  SQLite **and jj** need; virtiofs does") is superseded for jj by GS's result.
  That claim held for the SQLite tools (pnpm store index, Nx task DB) — which is
  why the *work surface* copies to ext4 (G1) — but jj's store is plain files
  (op-store, op-heads, git objects), and the gated `ATTRACTOR_VM_E2E` proves an
  in-guest jj commit lands in the host store over a plain **rw** 9p mount. So
  the store stays a shared rw mount, not virtiofs; GS's win (virtiofs gone) is
  intact. The only shared *rw* surface is the VCS store, and same-repo
  concurrency is still bounded to one live run per repo (unchanged from W2).
- **Colocation is required.** in-guest jj reaches the host store through the
  9p share of `$ATTRACTOR_REPO/.git`, so the target repo must be
  jj-**colocated** (a root `.git`). The launcher prechecks this (`jj root` +
  `.git`) and rejects an external-backend repo up front — otherwise its missing
  `.git` share would fail `mnt-repo-.git.mount`, the runner would never start,
  and the launcher would wait forever.
- **Upgrade note (virtiofsd orphans).** GS records no per-run daemons, but a VM
  in flight *across* the upgrade left a `vm.json` naming its `virtiofsd`. The
  reaper still kills those legacy `vfsd_pid`/`vfsd_pids` (read-only back-compat),
  so no operator drain is needed; the shim can be dropped once no pre-GS VM
  remains within the retention window.
