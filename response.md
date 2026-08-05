# Multi-lens review synthesis — `vzysrpqz..@` (T9a child-terminal fix)

Five lenses (correctness, design, prod_safety, simplification, tests) reviewed the change. **The lenses ran against a stale `@`.** The three high-severity defects they raised are already fixed inside the current reviewed range — fix commits (`uolzwrvu`, `nkyksvtr`, `koxwpxzw`) landed after the lenses ran. Verified against current code.

## Change summary

Adds `isChildEvent(ev)` (`Detail["source"]=="child"`) + `isOwnTerminal(ev)` (pipeline-terminal AND not child) so a *forwarded child* terminal no longer looks like the run's own. Gates three terminal sites so a nested child pipeline finishing/failing does not: close the parent SSE stream, mis-reconstruct interrupted-run history, or finalize the whole run on the phone-home ingest path.

## Resolved (fixed in current range)

| Finding | Lens(es) | Sev | Status |
|---|---|---|---|
| Phone-home `Ingest`→`finishFromEvent` ungated → child terminal finalizes whole run (permanent inconsistent manifest) | prod_safety, simplification #2 | BLOCKING | **Fixed** — `registry.go:740` gated `if isOwnTerminal(ev)` |
| Child stage events leak untagged synthetic `stage_failed` into parent graph (ghost/collided node) | correctness #1 | HIGH | **Fixed** — reconstruction skips `isChildEvent` (`registry.go:982`) |
| Re-entered looped node → duplicate synthetic `stage_failed` | correctness #2 | MED | **Fixed** — resolved-marking fires once (`registry.go:999-1002`) |
| Extract shared terminal predicate; kill per-site drift | design #3, simplification #1 | refactor | **Done** — `isOwnTerminal` at `registry.go:937`, used at `server.go:537`, `registry.go:740,986` |

All three terminal sites now route the "does this end the run?" decision through one predicate. Tests added (`phonehome_test.go`, `registry_restart_test.go`). `go test ./...` green.

## Still open (non-blocking)

1. **`source="child"` string triplicated, no shared const** *(design #1)* — `manager_loop.go:348` (producer), `registry.go:928` (Go consumer), `index.html:1895` (JS). Convention change → edit 3+ sites in lockstep. Go side should own `const detailSourceChild = "child"` referenced by producer, predicate, and tests.

2. **New SSE test half-unpinned** *(tests)* — `sse_child_terminal_test.go` uses a `RunCompleted` run, so `Subscribe` returns a *closed* channel; channel-close ends the stream, not the `server.go:537` guard. Mutation check: deleting the whole guard still passes; only deleting the `!isChildEvent` sub-check fails. Catches "child terminal closes early" but does **not** pin "parent terminal closes a *live* stream."

3. **No live-run SSE test** *(tests)* — nothing exercises `streamEvents` with `RunRunning` + open subscriber receiving a forwarded child terminal before the parent's. Caveat: `execute()` already closes live subscriber channels on terminal, so the `server.go` `return` may be belt-and-suspenders. Decide: add a live test or comment the guard as redundant.

4. **Minor cleanups** *(design #2, tests, simplification #3-5)* — `isChildEvent`/`isOwnTerminal` are pure `engine.Event` predicates sitting in `registry.go` (server package owns an engine/handler convention); `paintFromFrames` == `nodeStatesFromReplay` byte-identical (dedup); white-box `&Run{}` injection under mutex in new test (pre-existing package convention, `addRun` helper could absorb it); frontend `pipeline_completed`/`pipeline_failed` use two call paths for one decision (pre-existing, JS-side).

## Verdict: PASS

Reviewed code is sound and shippable. All blocking/high/med defects the lenses raised are already fixed in-range; `go test ./...` green. Open items are cleanups + one test-strength gap — none block a merge.

Suggested follow-up commit: shared `detailSourceChild` const (#1) + either a live-run SSE test or a redundancy comment on the `server.go` guard (#2/#3). Rest optional polish.
