# Local-First Plan — Strip Back, Then Build Up

Status: agreed 2026-08-13. Supersedes the implicit "daemon-first, VM-default"
direction. Companion reading: `docs/codebase-review-2026-08.md`,
`docs/spec/attractor.md`.

## Summary

A week in, attractor has not produced a green run outside of dogfooding. The
codebase is healthy (~15.8k non-test Go, well tested, spec-adherent), but the
*default execution path* runs through the most complex, least proven
infrastructure we have: daemon submit → placement → NixOS/QEMU VM → phone-home.
We are inverting that. The new unit of the system is a **self-contained local
run** that serves its own state; the daemon becomes an optional hub that
launches, watches, and archives runs. VMs return later as an opt-in isolation
wrapper around the exact same self-contained run.

The goal is the simplest working version that does what we need: one command,
one repo, one pipeline, green — with enough observability to debug it when it
isn't.

**Guiding principle: strip back and simplify as much as possible.** When a
decision point offers a simpler option, take it. Prefer deleting code to
maintaining it, deriving state to storing it, one contract to two, and
deferring capability until a real run demands it. Every phase below should
leave the system smaller or clearer than it found it, except where it adds the
one thing we lack: a working, observable run.

**Second principle: adhere to the original attractor spec.** The spec
(`docs/spec/attractor.md`) is the reference; deviations are either closed (D1
implements §3.5–3.6 as written), consolidated back onto it (D4 folds our
ad-hoc endpoints into the §9.5 surface), or recorded as explicit amendments
(per-visit stage dirs against §5.6). Extensions beyond the spec must be
documented as such, and we do not build capability the spec doesn't describe
without writing it down first.

## Why

### The complexity is real but narrow

Line audit (non-test Go):

| Area | Lines | Verdict |
|---|---|---|
| Core (engine, handler, dot, graph, condition, runstore) | ~4.6k | Lean. Keep. |
| Daemon (`internal/server`) | 4.7k | ~900 of it VM-specific (launcher_vm, reaper, placement) |
| Agent plumbing (acp, claudecode, hookshim, router) | ~1.4k | Two backends; ACP is primary |
| Built-ahead-of-need (items, automations/cron, interviewer, lint) | ~1.9k | Freeze |
| Dead (artifact store — faithful spec §5.5 impl, zero callers) | ~170 | Decide in Phase 3 |

Tests outnumber product code 1.6:1 (24.8k lines). The architecture is not
overcomplicated; the *default path* is. ~4k lines (VM + intake + cron + second
backend) were built before one external run succeeded.

### The failures we actually hit

1. **No retries, ever.** Spec §3.5–3.6 define retry with an error-classifying
   `should_retry` predicate (retry network/429/5xx/transient; never auth/400/
   validation). We implemented the retry machinery but: no predicate exists,
   both backends map every error to `FAIL`, only the human-gate timeout ever
   returns `RETRY`, and no shipped pipeline sets `max_retries` (default 0). Net:
   one transient ACP stall or rate limit permanently fails the node and usually
   the run. This is the most likely cause of "still no green run."
2. **Two execution dialects.** The daemon both embeds the engine in-process
   (direct launcher) and drives it as a subprocess over a phone-home HTTP
   contract (local/VM launchers). registry.go carries both lifecycles (1,280
   lines). Every feature pays twice.
3. **Push-based state replication is fragile.** The phone-home event shipper is
   fire-and-forget (`report.Forward` ignores per-event errors): a daemon blip
   silently drops events from the daemon's record. Making streaming replication
   reliable (cursors, acks, dedup) is real work that the pull model below makes
   unnecessary.

### What SSSF got right (github.com/disler/super-simple-software-factory)

We compared against SSSF (clone in scratchpad; visualizer read in full). Its
scope is far smaller — sequential phases, no sandbox, single host — but four of
its ideas transfer:

- **In-session correction over cold retry**: a malformed result triggers a
  corrective follow-up in the same context window; a retry throws away
  everything the agent learned.
- **Deterministic gates on non-deterministic work**: post-execution code checks
  verify the agent's *claims*, not predictions.
- **Append-only history**: every phase execution is a new immutable record;
  re-runs never overwrite.
- **Read-only observer UI** polling a cursor: trivially reconnect-proof.

## Target architecture (pull, not push)

```
attractor run --ui            # the unit: engine + read-only self-serving API/UI
        │                     # runs anywhere: laptop, remote box, inside a VM
        │ announce once ────▶ hub (optional): directory + scraper + launcher + archive
        │                     # hub pulls /pipelines/{id} + /events?since= from live runs
        └ on complete: tar run dir → ship to hub/S3 for the permanent record
```

- A run owns all its state locally (events.jsonl, stage dirs, checkpoint) and
  serves it over the same spec-shaped API whether or not any hub exists.
- The hub never ingests a live event stream; it scrapes. A hub outage cannot
  lose run data. Scrape failure doubles as the liveness signal.
- Discovery: whatever spawns the run records where it is; runs also announce
  once at start (a registration, not telemetry).
- Reachability is the accepted cost of pull: a run on an unreachable network is
  invisible until its archive lands. Auth on the run server comes later and
  unlocks remote runners.
- Human gates are answered on the run's own `/questions` + `/answer`; a central
  UI proxies to it. Notification integrations (Slack/Telegram) come later.
- VM runs archive **before** teardown: finish → tar → ship → ack → destroy.

This deletes, rather than adds: phone-home client, ingest, report streaming,
`--no-event-log`, the direct launcher, and registry.go's dual lifecycle all go
once the ladder below is climbed.

## Settled design decisions

**D1 — Error classification (spec §3.6 gap).** Backends classify at the
boundary: transport errors, stalls, 429/5xx-shaped provider failures →
`StatusRetry` with reason; auth/config/validation → `StatusFail`. Shipped
pipelines set `default_max_retries=2`. The existing retry/backoff machinery
(run.go, retry.go) finally executes.

**D2 — Contract checks + in-session correction.** Generalize the status.json
probe into a per-node list of deterministic post-execution checks, each
returning pass or a Violation carrying a correction prompt:

```go
type ContractCheck interface {
    Name() string
    Check(env HandlerEnv, outcome Outcome) *Violation // nil = pass
}
```

On violation, the handler sends the correction prompt as a continuation turn in
the same session (ACP session / `claude --resume`), bounded to 2 corrections,
then fails. v1 ships one check (status.json present + parses, honoring
`require_status`). Future checks slot in without touching the loop: declared
`artifacts="a.md,b.patch"` exist in the stage dir; later, re-running tests the
agent claims pass. Corrections apply to contract violations only, never to real
task failures.

**D3 — Visits and attempts are spans, derived from events.** Every node
execution attempt is already bracketed in the event log (`stage_started` carries
`Attempt`; closes at `stage_retrying`/`stage_completed`/`stage_failed`). Span
identity is `(node_id, visit, attempt)`. We add an explicit `Visit` field to
Event rather than inferring visit numbers by counting. Spans are a pure fold
over events — no new storage. Per-span stage directories are deferred but
planned: opening a historical span and reading its prompt/response is
necessary for debugging; it lands right after the waterfall exists (spec
amendment required). *(Landed as flat `{node_id}@v{visit}.a{attempt}/`
dirs — see `docs/spec/amendments/span-dirs.md`, which supersedes the
per-visit layout sketched here.)*

**D4 — API consolidation onto the spec surface (§9.5).** The enriched
`GET /pipelines/{id}` absorbs the derived state document (summary + spans +
active nodes + pending questions); `/state` and `/nodes/{node}` are dropped.
Kept extensions, documented as such: `/artifacts` (run-dir file serving, implied
by §5.6) and `/diff` (no spec equivalent; too useful to lose). Invariant that
makes this sound: everything in the enriched document is a fold over
(graph, events, run.json) — events remain the single source of truth, and any
client could reconstruct the document from `/events?since=0`. The single-run
server keeps `/pipelines/{id}/...` paths so hub and run speak one schema.

**D5 — Waterfall UI.** Read-only, polling `?since=` with a monotonic cursor
(500ms tick, catch-up loop, never stops; SSE unnecessary). Spans are
self-describing (`node_id`, `visit`, `attempt`, `thread_id`, `type`, `class`,
`model`, timestamps, outcome, tokens); lane grouping is purely a frontend
groupBy — by node, thread, class, or type — so it can be a dropdown, not a
backend opinion. Stolen from SSSF's visualizer: per-lane context-window
occupancy bar (we already account tokens in the ACP backend), tool-call tick
marks inside spans (red on error), min-width floor for tiny spans, status
glyphs with pulse on running, queued spans dashed, click-through to a
prompt/response/status detail panel, cost/runtime/token chips in the header.
Their overlap-shift layout assumes strictly sequential phases; ours has real
parallelism, so placement stays time-truthful per lane.

**D6 — Composition: static expansion vs dynamic dispatch (spec §9.4).** New
node form `type="subgraph"`, `graph_ref="path/to/child.dot"`, `var.*` for
context seeding. A load-time transform inlines the child (child's own
PromptFile runs first, node IDs prefixed `review.correctness`, child start/exit
spliced into the node's edges). The five static `review_loop` sites migrate;
the UI then shows the real five-lens fan-out. `stack.manager_loop` remains only
for children unknowable until runtime (router dispatch); its unused supervisor
machinery (poll/steer/cooldown/stop-condition) is stripped. Rule: child known
at parse time → subgraph; chosen at runtime → manager. No enforcement lint for
now (KISS).

**D7 — Artifact store: decide after D6.** `internal/artifact` faithfully
implements spec §5.5 and has zero callers, while review-core pushes five full
review texts through context (`parallel.results`) — exactly the bloat §5.5
exists to prevent. Merging (D6) changes how synth receives findings, so the
store's fate (wire it for parallel results and plans, or delete it) is decided
once the merged shape exists. Also unify naming: the server's `/artifacts`
routes serve the logs tree, not this store.

**D8 — Transport stays HTTP.** Unix sockets don't cross the VM boundary
without vsock; shared SQLite WAL corrupts over 9p/virtiofs; stdout-tailing is
one-way (gates need answers back). One HTTP contract spans local, VM, and
remote; under the pull model the hard streaming-reliability problem disappears
(archives are one-shot and idempotent). Sockets remain available later as a
transport optimization with zero protocol change.

**D9 — VMs return simplified.** Under pull, the guest just runs
`attractor run --ui`; the hub scrapes a forwarded port. No report tokens, no
ingest, no phone-home wiring in the image. Workspace materialization
(`workspace_revision`, per-run jj workspaces) is kept — it is launcher-level
and already works.

## Phases

### Phase 1 — Green local run (now)

1. **Error classification + default retries** (D1). TDD against the fake
   backend. Acceptance: a simulated transient backend failure retries with
   backoff and succeeds; an auth-shaped failure fails immediately.
2. **Contract checks + in-session correction** (D2). Acceptance: an agent that
   omits status.json gets a correction turn in the same session and the node
   succeeds; two failed corrections → node fails loud.
3. **Vars lint**: cross-check `$context.*` references against
   `vars=` ∪ `output_key`s ∪ well-known runtime keys; warn on undeclared refs
   and on declared-but-unused vars. Catches typos at validate time instead of
   mid-run.
4. **Run a real pipeline against a real external repo in local mode** (manual
   jj workspace, `--backend acp`, console gates). Fix what breaks. This is the
   milestone: green run, no daemon, no VM.

### Phase 2 — Observability

5. `Visit` field on Event (D3).
6. **Single-run server**: `attractor run --ui` mounts the spec API surface over
   the run's own logs dir; `/pipelines/{id}` enriched with spans (D4).
7. **Waterfall view** (D5) driving from events alone.
8. **Per-visit stage dirs** `{node_id}/v{N}/` + spec amendment (D3) — enables
   click-into-historical-span debugging.

### Phase 3 — Simplification

9. **Subgraph expansion transform**; migrate the five review_loop sites (D6).
10. **Slim manager_loop** to router's needs (D6).
11. **Artifact store decision** (D7).
12. **Retire the direct launcher**; daemon stops embedding the engine.

### Phase 4 — Scale back up

13. **Announce + hub scrape** — done. `attractor hub` (internal/hub): runs
    started with `run --announce <hub>` register once; the hub scrapes
    `/pipelines/{id}` + `/events?since=` from the run's own server, proxies
    questions/answers, and lists live + archived runs. Scrape failure is the
    liveness signal. `POST /pipelines` on the hub spawns
    `attractor run --announce` subprocesses (hub = directory + scraper +
    launcher).
14. **Archive-on-complete** — done. finish → tar.gz run dir → POST
    /pipelines/{id}/archive → ack; the unpacked archive is the permanent
    record, served through the same API schema as the live run.
15. **VM launcher returns** under the pull model (D9) — **remaining**. The
    guest just runs `attractor run --announce` with a forwarded port; then
    phone-home/report/ingest and the legacy serve daemon's dual lifecycle are
    deleted. Blocked on reworking the nix image + launcher; the legacy `serve`
    path (frozen) still covers VM runs until then.
16. **Remote runners** — done (mechanism). `run --ui-token` puts Bearer auth
    on the run's own server; the token travels in the announce and the hub
    presents it on every scrape/proxy, so a run on any reachable machine
    works.

## Frozen (not deleted)

VM launcher/reaper/placement and the nix image; daemon fleet mode and
phone-home; items intake (GitHub/Linear); automations/cron; router pipeline.
All hang off server routes with nothing depending on them. They return (or die)
in Phases 3–4 with evidence in hand.

## Explicit non-goals for now

- Reliable streaming event replication (obsoleted by pull + archive).
- Composition-rule lint, provider-lint parity for daemon submit (submit path
  changes in Phase 4 anyway).
- Slack/Telegram gate notifications (wanted, but after the waterfall).
- Multi-run scheduling, queueing, or placement policy beyond what exists.

## Sequencing rationale

Phase 1 needs no architecture and directly attacks the reason runs die. Phase 2
makes failures visible enough to debug the next ones. Phase 3 deletes weight
while the surface area is quiet. Phase 4 re-adds distribution on top of a unit
that is already proven to work alone. Each phase leaves the system in a
shippable state; nothing in Phases 1–3 is throwaway if Phase 4 is delayed.

## Postscript — the 2026-08-13 strip

Decided same day, executed on the `strip-daemon` branch: the tool is
`attractor run` + `attractor hub` (+ validate/render). Everything the
plan had frozen is now deleted rather than waiting on Phase 3–4
evidence — the daemon (`serve`, registry, launchers, web UI), items
intake (GitHub/Linear) and the router, `stack.manager_loop` (its last
user was the router; parse-time children use `subgraph`), phone-home/
ingest/hookshim (P4.15's deletion completed without a VM launcher:
VM orchestration is handled outside the tool — whatever boots the
machine runs `attractor run --announce` inside it), and cron
automations. Pull is the only model.
