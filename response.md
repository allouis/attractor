# Multi-lens review — `poszswwunrosqtwtqkkrkrvnnlwzrwsq..@` (T9a)

Scope: `internal/server/registry.go` + new `registry_restart_test.go`. Three commits (test → refactor extracting `reconstructHistory` → fix `resolveInterrupted`). All checks green: `go test ./...`, `gofmt`, `go vet`. No regression, no crash, no data-corruption, no race found. Merged from five lenses (correctness, design, prod-safety, simplification, tests), deduped.

## Verdict: PASS — no merge blocker

Nothing regresses existing behavior and the change ships its narrow goal. The findings below are (a) defects confined to the interrupted-run replay path (already a recovery path) and (b) incomplete coverage of the stated T9a goal. Strongly recommend the two Should-fix items and an explicit scope note before calling T9a "done."

---

## Should-fix

### S1. Repair only reaches the disk-reload path — misses same-process crashes *(correctness #3 + prod-safety #1)*
`reconstructHistory` (registry.go:918) short-circuits `if len(inMemory) > 0 { return inMemory }`, using empty in-memory history as a proxy for "reloaded from disk." But a run crashed **in the current process** finishes with a *non-empty, truncated* in-memory history:
- `failCrashed` (registry.go:408, live for local + VM launchers) sets terminal status and closes subscribers but **never appends a synthetic terminal event**.
- Later `Subscribe` → `finished` branch → non-empty `inMemory` → returns it raw, **bypassing `resolveInterrupted`**.

Result: node stuck "executing", no terminal event delivered → UI reconnects forever — the exact symptom T9a fixes, left unfixed for subprocess/VM crashes (arguably more common than daemon restart). Same root cause covers local-cancel-on-live-daemon. **Fix:** route both finished paths through `resolveInterrupted`, or have `failCrashed` append a synthetic terminal.

### S2. Duplicate synthetic `stage_failed` for looped (revisited) nodes — real bug *(correctness #1)*
`resolveInterrupted` builds `order` guarded by `if !running[ev.NodeID]`, and the synth loop **never resets `running[node]=false`** after emitting. Engine revisits nodes on loops (`state.visits[nodeID]++`, `max_node_visits`=2 in this very run). A 2nd `stage_started` after the first visit's terminal event puts the node in `order` twice → the synth loop emits `stage_failed` for it **twice**. Deterministic but duplicated. The `EventStageRetrying`→running branch guards the in-visit retry case, not the cross-visit loop case. **Fix:** clear `running[node]=false` after appending, or dedup `order`.

---

## Nits / lower-confidence

### N1. Synthetic-event fidelity *(correctness #2 + prod-safety #3)*
Synthesized events set only Kind/NodeID/Message/Seq. Real emits set `Timestamp: e.now()`, and real `stage_failed` sets `Status`.
- `Event.Timestamp` (`json:"ts"`, no `omitempty`) serializes `0001-01-01T00:00:00Z` → any time-ordered consumer (timeline/report) sorts synthetic terminals to year 0001, jumping them to the front.
- Synthetic `stage_failed` has empty `Status`; consumers keying on `ev.Status` get blank.
- Semantic mismatch: reloaded run has manifest status `cancelled` but replays `EventStageFailed`+`EventPipelineFailed` → graph shows red "failed" while summary says "cancelled". `EventStageFailed` also conflates interrupted with genuinely-failed.

### N2. Empty `events.jsonl` → no terminal delivered *(correctness #4 + tests missing-coverage)*
`resolveInterrupted` returns early on `len(history)==0`. Reloaded `running→cancelled` + empty/missing log → Subscribe yields zero events and closes → UI reconnect loop. Pre-existing behavior, but the new docstring claims the stream always signals done. Untested; likely a real gap.

### N3. Guard skips repair when a terminal pipeline event is present but a node stays open *(prod-safety #2)*
Early-return on first `pipeline_completed`/`pipeline_failed` (registry.go:947-948) means "terminal-present-but-node-open" (parallel branches, or engine flushing `pipeline_failed` before a node settles) is not repaired → that node paints stuck.

### N4. Cleanly-finished-but-missing-pipeline-terminal gets a red terminal *(prod-safety minor)*
All nodes resolved, no `running`, no per-node synth — but still appends `pipeline_failed`. Low probability (jsonl terminal flushed before manifest→completed), worth a comment.

### N5. `replayEvents` scanner (registry.go:993) *(prod-safety minor)*
1MB line cap, `scanner.Err()` unchecked. Pre-existing, but now a truncated-before-terminal log *triggers* synthetic-failure injection rather than returning partial history — consequence escalated.

---

## Design / architecture *(design lens; no blockers)*

- **D1. Node state-machine duplicated across 3 layers** — frontend `HANDLERS` (ui/index.html:1734), test `nodeStatesFromReplay` (registry_restart_test.go:43), and `resolveInterrupted`'s `running` map (registry.go:946). Comments admit the mirroring; they **already diverge** (frontend paints `stage_retrying→retrying`; the other two fold it into running). No single owner → each new event kind rots one copy. Follow-up: let the engine own the event→node-state mapping.
- **D2. Server fabricates engine domain events** (registry.go:964) — re-implements the engine's terminal-event contract (seq monotonicity, one-terminal-per-node, closing pipeline event) inside the server, breaching the engine-emits/server-persists boundary. Follow-up: `engine.SynthesizeTerminal(history)`.
- **D3. Interrupted-run repair split across two owners** — `reload` eagerly repairs *status* (running→cancelled, persisted); `resolveInterrupted` lazily repairs the *event stream* (never persisted, recomputed per Subscribe). Lazy is defensible; the split ownership is the smell — cross-reference at minimum.
- **Praise:** commit structure is textbook (test-first → pure refactor "sets the seam" → behaviour fix), independently reviewable. `reconstructHistory` naming/cohesion good. Double-guard (`!isTerminal()` + terminal-event scan) is genuinely non-redundant.
- Stale doc: `replayEvents` says "Used by SSE subscribers" — its only caller is now `reconstructHistory`.

---

## Simplification *(simplification lens; optional, no behavior change)*

- **P1.** Drop `maxSeq` scan — events.jsonl is seq-ordered, so `maxSeq := history[len(history)-1].Seq`.
- **P2.** Drop the in-loop terminal early-return — a terminal pipeline event is always the last line; check `history[len(history)-1]` once before the loop. Combined with P1, the loop collapses to a pure "which nodes still running" pass. *(Caution: interacts with N3 — simplifying here without addressing N3 preserves the terminal-present-but-node-open gap.)*
- **P3.** `!r.isTerminal()` guard is dead for the only caller (reload already forces reloaded runs terminal). Keep only as defensive contract.
- **P4.** Both tests duplicate the subscribe-collect loop → extract `collect(run)` helper.

---

## Tests *(tests lens)*

- Both new tests drive the public surface (`newRunRegistry().Get()`, `Subscribe(0)`, `Status()`) → survive internal refactor. Good.
- **T1. `nodeStatesFromReplay` reimplements the frontend painter in Go** (same duplication as D1) — tautological, gives false confidence. Assert the real Go contract directly on `got`: "every started node has a terminal event; none left running."
- **T2. `writeRunDir` hand-rolls the disk format**, bypassing `writeManifest()`/`appendEvent()` → can test a format production never emits. Seed via the real write path or share filename consts.
- **T3.** `TestReloadResolvesInterruptedRun` pins exact synthetic kind (`EventPipelineFailed`) — assert "some terminal pipeline event closes the stream," not the kind.
- **Missing coverage:** in-memory-preferred skip (never distinguished from no-op); interrupted + empty events.jsonl (N2, real gap); multiple concurrent running nodes (start-order determinism claimed but only one running node tested).

---

## Bottom line
Safe to merge as-is; no regression. Before treating T9a as complete, either fix **S1** (in-process crash path) and **S2** (looped-node duplicate), or explicitly scope T9a to "daemon-restart-from-disk only" in the milestone. N1–N2 are cheap fidelity fixes worth folding in.
