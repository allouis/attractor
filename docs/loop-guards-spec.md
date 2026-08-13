# loop-guards — catch futile runs early

## Problem

Two revise-pr runs (a5ac1389, 9afacdba — 2026-08-12) burned 5-8 review/fix
rounds each, hours of wall-clock, and an entire Claude session quota on
loops that could not converge. In both cases the ONLY guard that fired was
`max_node_visits` — the blunt terminal backstop — after the waste was
already spent. Root causes differed (stale review cache, then a
case-sensitive verdict parser), but the futility *pattern* was identical
and detectable by round 2:

- the same node failing with the **identical failure_reason** round after
  round (five consecutive `agent wrote no …status.json` in a5ac1389);
- **machinery failures routed as if they were verdicts**, sending a fix
  agent to "address" a harness error it cannot fix;
- a **session-limit error** (run 9afacdba round 8) failing the run
  terminally when the condition is temporary and self-heals at quota
  reset.

## What exists now

- `max_node_visits` — per-node visit cap; terminal backstop, keeps.
- D1 (landed alongside this spec): transient backend errors (429/5xx,
  stalls, network) classify to `StatusRetry`; the engine's per-node retry
  machinery re-runs with backoff under `default_max_retries`.
- D2 (landed alongside this spec): contract checks with in-session
  correction.
- Machinery labeling: a `require_status` miss is flagged "review machinery
  failed (not a verdict)" on both the node failure and the surfaced
  `stack.child.failure_reason` — but it still routes like an ordinary
  FAIL, so the fix node runs anyway.

## Design

### LG1 — machinery misses retry, then terminate; never route to fix

A `require_status` miss (never-written / invalid JSON / unknown outcome —
all carry the `(require_status node)` marker) becomes `StatusRetry`
instead of `StatusFail`:

- The engine's existing retry machinery re-runs the review node
  (bounded by the node's / graph's max_retries) — covering transient
  flakes (late writes, 9p latency) at the point of failure.
- Retries exhausted → the run FAILS with the machinery reason. It never
  takes the `outcome=fail` edge to a fix agent, because a harness error
  is not a code finding. a5ac1389 dies at round ~2 with "review machinery
  failing repeatedly" instead of five ghost-chasing fix rounds.
- manager_loop propagates the child's machinery-miss as retry the same
  way (its own node retries, then terminates).

### LG2 — engine stuck-loop breaker (the general net)

Engine-level, no pipeline opt-in: when the same node fails with the
**identical `failure_reason`** N consecutive times (N=3 default; graph
attr `max_repeated_failures` to tune), the run aborts:

    stuck loop: node "review_loop" failed 3 consecutive times with the
    same reason — no progress is being made. Reason: …

- "Identical" = exact string match after trimming; cheap and unambiguous.
  A fix loop making real progress produces *changing* failure reasons
  (different findings each round) and is never touched.
- Catches every future variant of "routing on a stale/unchanging signal"
  regardless of cause — parser bugs, cache bugs, prompts that ignore
  feedback — without knowing the cause.
- The engine already records per-node outcomes; the check is a string
  compare on the last N.

### LG3 — quota/session limits park the run (backlog, needs design)

Backend errors matching rate/session-limit shapes ("session limit",
"resets &lt;time&gt;", 429-with-reset) should PARK the run — a paused state
awaiting operator resume — rather than fail it terminally. Depends on the
revive machinery (resumable cancelled/failed runs with host-side
checkpoints, landed 2026-08-12) plus: a `parked` run status, a resume
control, and optionally auto-resume at the advertised reset time. Design
sketch only; not in this milestone set. Interim mitigation: LG1/LG2 make
quota burn far less likely by killing futile loops early.

## Non-goals

- No cost/token budgeting (separate concern; belongs with the daemon).
- No semantic "is the fix agent making progress" judgment — LG2's exact
  string match is deliberately dumb; anything smarter is a review-quality
  problem, not a loop-guard problem.

## Milestones

| ID | Milestone | Status |
|----|-----------|--------|
| LG1 | require_status misses → StatusRetry, bounded by node retry policy; exhausted → run fails with machinery reason; never routes to fix. manager_loop child machinery propagates the same way. TDD incl. the a5ac1389 replay shape | todo |
| LG2 | engine stuck-loop breaker: N identical consecutive failure_reasons on one node aborts the run (default 3, `max_repeated_failures` graph attr); progress loops (changing reasons) untouched. TDD | todo |
| LG3 | parked-run design for quota/session-limit errors (spec addendum only in this set) | todo |
