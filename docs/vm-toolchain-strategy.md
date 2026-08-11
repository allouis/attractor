# VM toolchain strategy: the guest is generic Linux

How the VM runner image stays working when a target repo bumps its
toolchain (node, pnpm, playwright, …) — decision **GL**, established by
getting TryGhost/Ghost's full test suite and dev app green in-guest
(2026-08-11). The companion history of what broke and why is in the
commit "feat(vm): make the guest a CI-parity generic-Linux box".

## The principle

**The image never pins a repo's toolchain. It makes any repo-pinned
toolchain work.** Modern repos carry their own version pins:

| Pin | Mechanism | Who fetches it |
|---|---|---|
| pnpm version | `package.json` `packageManager` | corepack, per invocation |
| node version | `package.json` `devEngines.runtime` (`onFail: download`) | pnpm, per invocation |
| browser builds | playwright's version-keyed browser registry | `playwright install`, per run |

All three fetch **generic dynamically-linked Linux binaries** at run time.
The image's only jobs are (a) a bootstrap toolchain to start that chain
and (b) making generic Linux binaries run on NixOS.

## What the image provides

- **Bootstrap node = current LTS** (`pkgs.nodejs_24`), used ONLY to run
  corepack and as a fallback for repos that pin nothing. Bump it whenever
  a new LTS lands; nothing repo-facing depends on its exact version.
- **`pnpm`/`pnpx` as corepack shims** — never a nix pnpm. Package scripts
  re-invoke bare `pnpm`; the shim makes every nesting level resolve the
  repo's pinned version. (A nix pnpm 10 against Ghost's pinned 11 produced
  real breakage: different virtual-store layout, devEngines violations.)
- **nix-ld with the full browser library set** (chromium + firefox +
  dlopen'd ffmpeg for H.264) plus `PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS`
  — playwright's ldd probe can't see nix-ld's loader, but the libraries are
  there. This is what makes *any* playwright version's downloaded browsers
  run without image changes.
- **Real fonts including corefonts** (Arial): browser-test assertions
  depend on font metrics (Ghost's koenig caret tests; its CI installs
  msttcorefonts for the same reason).
- **CI-parity env**: `CI=true`, `TZ=UTC` — repo suites are tuned for
  hosted CI (retry policies, TZ-sensitive tests, reporter selection).
- **Git metadata for the workspace**: jj materializes tracked files only,
  so the runner builds an isolated `.git` (depth-1 fetch of the workspace
  commit from the shared host git dir) and initializes submodules over the
  network (Ghost's default themes are submodules; the app 500s without
  them).

## When something bumps

- **Repo bumps node/pnpm**: nothing to do. corepack + devEngines fetch the
  new versions on the next run.
- **Repo bumps playwright**: nothing to do *unless* the new browser build
  links a library outside the nix-ld set. Symptom: `error while loading
  shared libraries: libfoo.so` in the check output. Fix: add the library to
  `programs.nix-ld.libraries` in `nix/vm-runner.nix`, rebuild `.#vm-runner`.
- **New LTS node released**: bump `pkgs.nodejs_24` → `nodejs_26` at
  leisure; it's bootstrap-only.
- **A repo needs a new runtime family** (e.g. Ruby): add the bootstrap
  package to `environment.systemPackages` (decision D8) — same principle,
  the repo's own version manager should do the real pinning where one
  exists.

## Known wrinkles (2026-08)

- **Ghost pins pnpm 11.15.1, which has an install bug**: with Ghost's
  lockfile it silently skips `bindings@1.5.0` (an optional-flagged
  transitive dep of better-sqlite3) → 135 ghost-core stats tests fail with
  `Cannot find module 'bindings'`, and the failures get MASKED as a vitest
  "unhandled errors" sourcemap cascade (vitest trips over a
  `sourceMappingURL=` string inside tsx's dist while formatting them).
  pnpm 11.21.0 installs the same lockfile correctly, so Ghost's checks
  config pins the install step to it (`corepack pnpm@11.21.0 install
  --frozen-lockfile --pm-on-fail=ignore`). Drop the pin when Ghost's
  `packageManager` moves past 11.15.1.
- **Ghost's `test` needs a `build` first**: the nx `test` target has no
  `dependsOn: build`, so a fresh workspace fails typecheck-style test
  substeps on unbuilt workspace deps. Ghost's checks `test` command is
  `corepack pnpm run build && corepack pnpm run test` (build is nx-cached,
  and the nx cloud read cache makes it cheap in-guest).
- **Host-side jj + blobless clones**: `/home/agent/Ghost/.git` is a
  `blob:none` partial clone. `git` promisor-fetches blobs on demand; jj
  (gitoxide) cannot, so after `jj git fetch` a checkout of new commits dies
  on missing blobs until `git backfill` runs. If a repo checkout is going
  to be driven by jj, keep it backfilled (or clone without a blob filter).
