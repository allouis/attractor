# Codebase review — August 2026

Deep review of coupling/cohesion, clean code, dead code, and professionalism,
plus design exploration of the three main architectural problems. Produced by
a multi-agent review (six parallel review lenses, then three design-options
agents); findings verified against the code at the time of review. File:line
references are as of commit 268709d and will drift.

## Overall verdict

**Professional, disciplined codebase. Grade A-/B+.** ~14k production LOC plus
~15k test LOC, zero external dependencies (two-line go.mod, no go.sum; builds
with `GOPROXY=off`). `gofmt`, `go vet`, `go build` all clean. Zero panics
outside main, zero `init()`, zero `context.TODO`, zero TODO/FIXME comments in
code, near-100% doc-comment coverage in Go convention.

Dead code is near zero: three truly dead functions (~13 LOC) — `Conn.Done` /
`Conn.Err` (internal/acp/conn.go), `NewVMLauncher` (launcher_vm.go) — plus
ten functions (~55 LOC) alive only via tests. One hygiene issue: `serve.log`
is committed and should be untracked. `NewVMLauncher` +
`materializeWorkspace` look like a partially-unwired VM feature rather than
cruft; make a design decision before deleting.

## Architecture (coupling/cohesion): B+

The dependency graph is acyclic and layered correctly: `dot` → `graph` →
`engine` → backends/handlers via interfaces → `server`/`cli` as composition
roots. `engine` imports nothing above it; `config` does not leak (4
importers). The backend seam (`CodergenBackend`) has four real
implementations plus a router — a justified Strategy, not speculation.
`cmd/attractor/main.go` is a textbook thin entrypoint.

Two structural debts:

1. **`server.Run` god object** — registry.go (965 lines), ~30 fields, ~40
   methods across six responsibilities: lifecycle state machine, SSE pub/sub,
   disk persistence, human-gate Q&A, restart/resume, checkpoint reads. The
   terminal-transition critical section is copy-pasted 4× (`execute`,
   `failCrashed`, `finishFromEvent`, partially `Cancel`) — changing terminal
   semantics is shotgun surgery.
2. **Duplicated run identity between engine and server** — two `Manifest`
   types (engine/types.go vs server/registry.go), two `writeManifest`
   implementations, and `server.newRunID` re-implementing ID generation that
   engine already exports (note: server uses 16 hex chars, engine 12 — not
   byte-identical).

`internal/server` as a package is large by aggregation (~15 responsibilities
across 16 files) but files are split by concern; the god *object* is `Run`,
not the package. Extracting `internal/run` (domain) and `internal/launcher`
is worthwhile eventually but not urgent (see design options below).

## Sharpest concrete findings

1. **Data race: parallel branches share one stage dir**
   (handler/parallel.go ~L86-90). `subEnv.Context` is cloned per branch but
   `subEnv.Stage` still points at the parent's store, so concurrent branches
   write `status.json` / `prompt.md` / `response.md` into the same directory.
   Fix: per-branch sub-store.
2. **Manifest clobbering on the direct launch path — active data-loss bug.**
   On the default in-process launcher, daemon and engine write the *same*
   `manifest.json`: daemon writes the full schema with `id`
   (registry.go:656), then the engine — created with
   `LogsRoot: r.logsRoot` (registry.go:660) — clobbers it with its id-less
   schema for the whole run duration, and the daemon restores it only at
   completion (registry.go:687). `reload` skips id-less manifests
   (registry.go:93), so a daemon restart mid-run loses the run from the
   fleet. Local/VM launches write to separate filesystems and do not
   collide.
3. **`writeManifest` is non-atomic** (registry.go:904 plain `os.WriteFile`;
   `runstore.Write` is equally non-atomic). Crash mid-write → truncated
   manifest → silently dropped on reload. `rotateEventLog` and config `Save`
   already do temp+rename correctly — inconsistency, not ignorance.
4. **Server bypasses the runstore seam.** `internal/runstore` is documented
   as "the single seam through which Attractor writes run artifacts" and
   enforced by tests/guard_fswrite_test.go — but only for
   engine/handler/backend. The server does raw `os.WriteFile`/`os.OpenFile`
   and hand-rolls path-containment guards 4× with subtle differences
   (getArtifact has EvalSymlinks, putArtifact does not; workflowDir roots at
   the catalog).
5. **`Outcome.Status` dual representation** (engine/types.go ~L65-83): enum
   with `json:"-"` plus a parallel `StatusString` forces manual
   `ParseStatus` re-attachment at 4+ deserialization sites; missing one
   yields a silent `StatusUnknown`. Fix: `MarshalJSON`/`UnmarshalJSON` on
   `Status`.
6. **Client DTO drift — already happened.** `client.RunSummary`
   (internal/client/types.go) is missing five fields the server emits
   (`needs_human`, `resumable`, `workflow_name`, `repo`, `vars`);
   `client.Event` omits `Question`. Nothing broke because `internal/client`
   has zero production importers (it pre-landed for a future TUI). The only
   live JSON consumer is the web UI — hand-written JS in
   internal/server/ui/index.html, immune to Go-side typing.
7. **Path traversal via artifact id** (internal/artifact/artifact.go
   L75/107/155/166): `id` is interpolated into filesystem paths
   unsanitized. Reachability depends on callers; guard anyway.
8. **Long functions**: engine `Run` loop ~100 lines of mixed phases
   (run.go:164-264); `executeNodeWithRetry` ~90 lines (run.go:429);
   `manager_loop.Execute` 155 lines / six jobs; server `submit` has nine
   positional string parameters (server.go:355).
9. **Inconsistent HTTP error shape**: 41 sites use plain-text `http.Error`,
   one returns JSON `{"error": …}` (config_api.go). Add a `writeError`
   helper.
10. **Logging story is thin**: bare `log.Printf`, four call sites, no
    levels/structure. Weakest idiom area for a long-running daemon;
    consider `slog`.
11. **Triplicated handler-type list**: lint.go:353, lint.go:421,
    cli.go:733 — registering a new handler and forgetting a lint map yields
    spurious warnings.
12. **Smaller items**: CLI hardcodes daemon-owned filenames
    (cli.go ~L290 `daemonOwnedArtifact`); `Serve` ends in `select {}` with
    no signal-driven graceful shutdown; dead `Diagnostic.Fix` field
    (lint.go:52); dead `resuming` variable
    (backend/claudecode/claudecode.go ~L76-82); backend/claudecode and
    backend/acp duplicate the session-map, stderr-decoration, and scanner
    buffer-size patterns.

## Tests: B+

- `internal/server` is heavily tested in-package (6k test LOC vs 3.2k
  source) — the risk-dense area is covered, including XSS, traversal,
  races, SSE reconnect.
- **Best thing in the repo**: tests/guard_fswrite_test.go — an
  architectural fitness function that walks engine/handler/backend and
  fails on any raw filesystem write not annotated `// runstore:allow`;
  guard_workdir_test.go pins the work-dirs-unchanged invariant.
- Gaps: the `e2e_` prefix overstates scope (most files are in-process
  integration tests against the fake backend; true subprocess e2e is ~5
  files). Zero `t.Parallel`; only six `t.Run` subtests repo-wide — 32
  table-loop files have unnamed cases where the first failure aborts the
  rest. Thin direct coverage: interviewer, artifact, ingest, toml, setup,
  hookshim, automation.

## Genuinely good (keep as-is)

- `internal/lint`: 14 small `Rule` structs — textbook Strategy /
  open-closed. The standout package.
- `internal/runstore`: clean capability object (ironically unused by the
  server — see finding 4).
- `internal/acp`: watchdog armed only after session setup; conn.go is a
  disciplined minimal JSON-RPC with correct mutex split and documented
  re-entrancy contract.
- Registry SSE channel discipline (`Subscribe`/`Cancel`/`deliver`) is
  above-average, with edge cases commented.
- stdout/stderr discipline (warnings to stderr so `--json` stdout stays
  machine-clean); error strings uniformly lowercase and `%w`-wrapped;
  atomic config `Save`; per-run phone-home tokens (least privilege);
  `items/httpapi` behind a narrow `Deps` interface.

---

# Design options for the three architectural problems

## Problem 1: `server.Run` god object

**Hard constraint**: `status` ⊕ `subscribers` ⊕ `history` ⊕ `questions`
form one atomic domain under one mutex. `Subscribe` (registry.go:314-354)
reads status and registers under the same lock; splitting the mutex lets a
subscriber register after the terminal close ran → its channel never
closes → SSE client hangs forever. **The mutex may not be subdivided.**
Migration cost is dominated by ~10-15 white-box test files that construct
`&Run{...}` literals, not by production code.

| Option | Shape | Risk | Effort |
|---|---|---|---|
| A: Extract `finish()` | One method owns the terminal critical section; `execute`/`failCrashed`/`finishFromEvent` compute (status, outcome, failure) and call it; `Cancel` keeps its double-close guard and shares `closeSubscribersLocked` | ~zero; no test breaks | S |
| B: Extract Class, mutex stays in `Run` | `manifestStore` (pure I/O, cleanly separable and unit-testable), `broadcaster` + `questionBook` as lock-agnostic collaborators (`deliverLocked` etc., caller holds `Run.mu`) | ~10 test constructors break | M |
| C: New `internal/run` package | Domain (Run + registry + persistence) out of the HTTP package; `Launcher`/`HandlerFactory` become boundary interfaces | ~15 white-box test files move; import untangling | L |
| D: Event-sourced status | Derive status from events.jsonl | Cancel/crash emit no events today; needs a runtime projection anyway | reject |

**Recommendation**: A now; then B's `manifestStore` slice; defer C until a
second (non-HTTP) consumer of the run domain exists. A is genuinely step 1
of both B and C — nothing is wasted.

## Problem 2: Run persistence / runstore / manifests

| Option | Shape | Fixes fleet-loss bug | Effort |
|---|---|---|---|
| A: Hygiene | Share ID generation (parameterize byte count — do not shrink server's 16-hex IDs), `atomicWrite` in server, one shared containment helper | no | S |
| B: runstore owns writes | Add `runstore.WriteAtomic` (temp+rename); server gets a per-run `runstore.Dir`; extend guard_fswrite_test to internal/server (launcher-infra writes need `// runstore:allow` or a subpackage) | no (torn writes only) | M |
| C: Single manifest ownership | Separate filenames: daemon owns `manifest.json` (fleet record, keeps `id`); engine writes `run.json`. No shared path → clobber gone. Keep JSON field names so old dirs load unchanged | **yes** | L |
| D: Typed `runmeta` seam / route direct launches via phone-home | Larger unification | yes | XL, defer |

Caveat: `runstore.resolve` *rejects* absolute/`..` paths where the HTTP
guards *neutralize* leading slashes — routing guards through runstore is a
minor observable behavior change (400 instead of served).

**Recommendation** (small independent commits): (1) kill run-ID
triplication, (2) `runstore.WriteAtomic` + use for engine
manifest/checkpoint, (3) containment helper, (4) server writes through
`runstore.Dir` + extend the guard test, (5) manifest ownership split — the
actual bug fix. Steps 1-3 are independent; 4 and 5 need 2.

## Problem 3: Client/server DTO contract

| Option | Drift protection | Effort |
|---|---|---|
| A: Conformance test | Test-time; the keyset-subset variant catches new-field drift and would be red today (the five-field gap) | S |
| B: Shared `internal/api` leaf package | Client↔wire becomes compile-time; engine→api drift concentrates in one reviewable converter | M |
| C: Client imports engine/server types | Inverts the dependency (thin client → engine → half the codebase); violates the client's stated stdlib-only design; can't cover `RunSummary` anyway (it's a `map[string]any`) | reject |
| D: Typed server responses | No protection alone but fixes the map-vs-struct inconsistency and is the enabler for B. Every conditional field maps to `omitempty`; only wrinkle is zero `time.Time` (use a pointer) | M |

**Recommendation**: A now (one file; immediately surfaces the existing
five-field drift, then fix those fields). D→B when the TUI actually lands.
Beyond the tripwire, this problem is not worth solving yet: single team, no
client consumers, and the web UI is JS that no Go typing protects.

---

# Combined roadmap (small atomic commits)

**Now (all S, independent, near-zero risk)**
1. Extract `finish()` in registry (kills the 4× terminal duplication)
2. `runstore.WriteAtomic`; engine manifest/checkpoint use it
3. Unify run-ID generation (length-preserving)
4. Containment-check helper for the four guards
5. DTO conformance test (will be red → fix the five missing fields)
6. Per-branch stage dir in parallel.go (the data race)
7. Untrack `serve.log`; delete `Conn.Done`/`Conn.Err`, dead `resuming`
   variable, dead `Diagnostic.Fix` field

**Next (M)**
8. Server writes through `runstore.Dir` + extend guard_fswrite_test
9. Extract `manifestStore` from `Run` (snapshot under lock, persist outside)
10. Typed server responses
11. `Status` self-serializing (MarshalJSON/UnmarshalJSON)
12. Engine `Run` loop / `executeNodeWithRetry` / `manager_loop.Execute`
    function extractions; `submit` parameter object
13. `t.Run` in table tests; `writeError` helper; `slog`

**The real bug fix (M/L)**
14. Manifest ownership split: daemon owns `manifest.json`, engine writes
    `run.json`

**Defer until pulled by need**
- `internal/run` package (needs a second domain consumer)
- Shared `internal/api` DTO package (needs the TUI)
- Event-sourced status (rejected)

The pattern across all three problems: the seams are mostly already right —
the fixes are "route through the seam that exists" (runstore,
engine.NewRunID, the typed structs other endpoints already use), not
"invent new architecture." The cost driver everywhere is white-box test
coupling, not production code.
