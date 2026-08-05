# Multi-lens review — T8b child-findings routing

Diff routes forwarded child `pipeline_failed` events into collapsible **Review notes**
instead of the red failure banner. The `pipeline_failed` path is correct and tested.
But the fix is **asymmetric** and breaks the parent stream on the happy path of the
exact feature it targets. **Do not merge as-is.**

## 🔴 BLOCKING — child `pipeline_completed` unguarded → parent stream dies early
*(correctness + prod_safety agree independently)*

`consumeChildEvents` forwards **every** child event kind onto the parent SSE stream
tagged `source=child` (`handler/manager_loop.go:347-350`), including the child's
terminal `EventPipelineCompleted` (`engine/run.go:183,240`). The stream is keyed by
the **parent** run path, not by the event's RunID (forwarded events keep the child's
RunID; `run.go:700` only fills empty RunID), so `source` is the only discriminator.

The diff guards `pipeline_failed` on `source=child` but leaves `pipeline_completed`
unguarded:

```js
pipeline_completed: () => { $('run-review').innerHTML = reviewNotesHtml(reviewNotes, true); done(); loadArtifacts(); },
```

Break sequence — manager_loop child review **passes** (common case, fix loop resolves
findings):
1. child emits `pipeline_completed` → forwarded → parent UI fires this handler mid-run
2. `done()` closes the EventSource + kills the dock poll (`index.html:1725-1729`) — no reconnect
3. marks notes **resolved**, calls `loadArtifacts()` before downstream artifacts exist

Parent still running, UI frozen at "completed / resolved." **Every later parent event —
including a real parent `pipeline_failed` — is never seen → silent failure masking.**
Loop makes it worse: the first passing child iteration kills the parent view.
Directly contradicts the diff's own comment *"Only the parent failure is terminal for
the stream."* Child success is now terminal too.

**Fix:** gate **both** terminal handlers on `source`. Generalize `classifyPipelineFailed`
into a `source`-aware `classify(ev)` used by `pipeline_completed` and `pipeline_failed`
alike; a `source=child` `pipeline_completed` must be a no-op for stream termination.

```js
pipeline_completed: ev => {
  if (ev.detail?.source === 'child') return;   // child done ≠ run done
  renderReviewNotes(true); done(); loadArtifacts();
},
```

## 🟡 `resolved` is asserted, never verified *(prod_safety)*

`pipeline_completed` unconditionally renders `reviewNotesHtml(reviewNotes, true)`,
assuming the fix loop resolved child findings — a topology assumption baked into the UI.
An advisory review that surfaces findings and succeeds **without** fixing them gets a
false green "resolved" stamp. Same root as the blocker: UI infers child semantics from
the parent's terminal event kind.

## 🟡 Test gap — the broken path is untested *(correctness + tests)*

New tests exercise only the pure helpers (`classifyPipelineFailed`, `reviewNotesHtml`)
in isolation. Nothing drives the handler wiring, so the green suite proves nothing about
stream termination. Inlining `classifyPipelineFailed` (behaviour identical) breaks 3
asserts; breaking the handler wiring breaks 0 — tests coupled to the seam, blind to the
wiring. No coverage of `pipeline_completed` with `source=child`, of stream-not-terminated
on child failure, of reset between runs (`index.html:1619` — notes leak across runs), or
of accumulation (N events → N `<li>`).

**Fix:** add one `fire()`-harness test (mirror `timeline_wiring_test.go`) feeding a
child-sourced `pipeline_failed` then `pipeline_completed`, asserting: `#run-review`
populated + resolved, `#run-failure` empty, **stream not closed**, notes cleared on the
next `openRun`.

## Non-blocking — clean-code / simplification *(design + simplification)*

- **`classifyPipelineFailed` returns `{section}` over a boolean** — `'failure'` value
  never read. Collapse to a predicate `isChildFinding(ev) => ev.detail?.source === 'child'`
  (keep it named + tested for TDD value), and reuse it to gate the completed handler.
- **Magic string `'child'`** hardcoded in JS + 3 test spots — hoist to a named const;
  document the cross-module contract next to the emitter (`consumeChildEvents`).
- **Duplicated `$('run-review').innerHTML = reviewNotesHtml(...)`** (lines ~1742/1750/reset)
  — extract `renderReviewNotes(resolved)`.
- **Child rationale repeated 4×** (inline comment, function doc, test docs, memory) —
  keep once on the predicate.
- **Temporal coupling** — `reviewNotes` reset in `openRun` sits ~130 lines from push/render;
  matches existing `nodeLog`/`nodeState` global pattern but easy to forget on new state.
- **Stale accumulation** — `reviewNotes.push` across loop iterations, no dedup/attribution:
  iteration-1 findings already fixed by iteration-3 render together as current.
- **`.banner.review`** reuses the `banner` base for a "not-a-banner"; name lies (minor).
- **Changeset cohesion** — unrelated workflow-table styling (`w-px`, `text-right` on `<th>`)
  bundled in the range; split into its own commit.

## ✓ Confirmed correct
- `pipeline_failed` child routing; non-child / `source:"phone"` → red banner (tested).
- Parent's own `pipeline_failed` (`detail=nil`) → `child=false` → red banner + terminal.
- XSS escaping via `escapeHtml` (tested).
- Empty notes → nothing rendered.
- Replay dedup via the `applied/pos` cursor (`index.html:1707-1714`) is idempotent across
  reconnects; `openRun` resets `reviewNotes` before a fresh attach.
- CSS/`tailwind.css` changes are regenerated cosmetic output.

**Verdict: FAIL.** The unguarded `pipeline_completed` handler masks production failures on
the happy path of the manager_loop child review this change targets. Gate both terminal
handlers on `source`, add the integration test, and downgrade the unconditional `resolved`
claim before merge.
