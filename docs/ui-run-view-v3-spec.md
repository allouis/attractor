# ui-run-view-v3 — a run view you can trust

## Problem

Watching a live run today (2026-08-11, run d3199c0d, Ghost/HKG-1914 in a
VM) the run view failed its one job — telling the operator what is
happening — in six concrete ways:

1. The graphviz SVG renders at natural size: it sprawls over two+
   screens, dominates the page, and is unstyled default graphviz.
2. The Diff section always says "no diff": the daemon deliberately skips
   its diff probes for VM runs (server.go, launch stamping) even though
   in-guest jj commits land in the shared host store where a diff is
   computable.
3. The only readable view of run activity is clicking the raw
   `events.jsonl` artifact. The rich stream (tool calls, agent prose)
   renders solely in the node inspector, only after clicking a node, and
   nothing indicates that.
4. The bottom Events timeline shows lifecycle rows only ("stage_started
   plan") — redundant with the graph, answers no operator question.
5. Clicking a node shows "stage detail unavailable" on every VM run:
   stage files live on the guest disk; the daemon's `/stages/{node}`
   404s.
6. Refreshing the page changes which node shows as "Active": the UI
   re-derives pipeline state client-side by replaying the SSE stream
   into ad-hoc accumulators, and the derivation is order-sensitive.

Plus: the header shows only the run id. Repo, workflow, issue
identifier/title/url all exist in the manifest and vars and are rendered
nowhere.

Root causes, not symptoms:

- **The daemon never exposes its authoritative state.** Registry status,
  engine checkpoint, and the event log all exist server-side, but the UI
  reconstructs pipeline state in the browser. Every refresh bug is this.
- **VM/subprocess runs have a different (poorer) API surface** than
  in-process runs — stages 404, diff empty — and no test asserts parity,
  so the gaps shipped silently.
- **The event stream — the actual product of a run — has no first-class
  rendering.**

## Design stance

The event stream is the product; the graph is a map. The UI is
**hydrate-then-append**: it loads server-computed state, renders it, and
lets SSE only append feed entries. Nothing user-visible is ever derived
client-side from event replay. Refresh is idempotent by construction.

State derivation is subtle (a failed stage means started−completed set
algebra lies about the active node) — it is computed exactly once,
server-side, next to the data.

## Phase 0 — correctness (no pixels until this is green)

### P1 — the state + node endpoints

`GET /pipelines/{id}/state` returns one server-computed document:

```jsonc
{
  "id": "…", "status": "running",
  "repo": "TryGhost/Ghost", "workflow": "implement",
  "item": {"identifier": "HKG-1914", "title": "…", "url": "…",
           "source": "linear"},
  "placement": {"runner": "vm", "image": "default"},
  "started_at": "…", "completed_at": null,
  "active_nodes": ["implement"],          // server-derived, fail-aware
  "nodes": {
    "plan": {"status": "completed", "attempts": 1,
              "started_at": "…", "completed_at": "…",
              "outcome": "success"},
    "test": {"status": "failed", "attempts": 1, "…": "…"},
    "open_pr": {"status": "pending"}
  },
  "questions": [ /* pending, same shape as /questions */ ]
}
```

`GET /pipelines/{id}/nodes/{node}` serves what is knowable **before the
node runs** — from the prepared graph the daemon already holds (and
ships to the guest): type, **resolved prompt text**, timeout, retry
policy, llm_provider/llm_model, edges in/out with conditions/labels.
Available from run start for every node.

Both endpoints work identically for direct, local, and vm runs — that is
the point.

### P2 — diff for subprocess/VM runs

Drop the VM skip. Stamp the base change id at launch (already done for
direct); the tip is the run workspace's change in the **shared host jj
store** (in-guest commits land there by design — vm-workspace-spec W2).
`GET /pipelines/{id}/diff` serves `jj diff --from base --to tip` for
every runner. Empty diff means "no commits yet", rendered as that, not
as "no diff".

### P3 — stage-output phone-home

At `stage_completed`, the child pushes the stage's output files to the
daemon over the existing phone-home artifact channel; the daemon stores
them under the run's stage dir so `GET /stages/{node}` works for every
runner:

- codergen stages: `response.md` + `status.json`.
- **tool stages: `stdout.txt` + `stderr.txt` + exit code.** The tool
  handler already writes these to the stage dir (tool.go) — today they
  are guest-local for VM runs and rendered nowhere for any runner, so
  the full lint/test output an operator most wants is invisible
  (`stage_completed` carries only status + duration; a failing check's
  failure_reason truncates stderr to 200 chars).

Prompts do NOT phone home — they are served up front by P1's node
endpoint. The streamed events remain the live transcript; the stage
files are the verbatim record.

### P4 — runner-parity API tests

The `local` launcher reproduces the VM's data path (child subprocess,
`--report-to`, no daemon-side engine) without KVM. Add an e2e suite that
runs the same pipeline under `direct` and under `local` and asserts the
**same API surface**: /state (including active_nodes across a
fail-and-retry), /nodes/{node} prompts, /stages/{node} after completion,
/diff after an in-run commit, /questions round-trip. This is the
regression net that was missing; it must fail on today's daemon.

## Phase 1 — the run view, rebuilt

Built strictly on Phase 0 data. The existing run view is replaced, not
patched — its client-side state accumulators are deleted.

### R1 — header card

Repo, issue (identifier + title, linked to the tracker url), workflow,
runner + image, status pill, elapsed time (live), run id. All from
/state. One glance answers "what is this run".

### R2 — the feed

Replaces the Events timeline as the page's main surface: the whole run
rendered as a conversation-like feed, newest at the bottom,
auto-scrolling while live (scroll-up pauses following).

- Agent prose → message bubbles (per node, node-name chip on the group).
- Tool calls → compact monospace rows, collapsed by default behind a
  per-group "N tool calls" toggle (prose-forward default).
- Lifecycle (stage start/finish, retries, checkpoints) → thin divider
  rows.
- Tool/check stages → an output block: command, exit code, duration
  visible; full stdout/stderr collapsed behind a toggle. A **failed**
  check auto-expands its output tail — the thing you need is on screen
  without a click.
- **ANSI rendering**: captured outputs carry SGR escapes (nix, pnpm, Nx
  all colorize). Stage files store the raw bytes (verbatim record); the
  UI renders them — HTML-escape first (content is attacker-controlled),
  then map the common SGR subset (16/256 fg colors, bold, dim, reset) to
  theme-aware spans and strip everything else (cursor movement, OSC) so
  no raw `\x1b[` garbage reaches the operator. Applies wherever stage
  output renders: feed blocks, inspector, and the artifacts file viewer.
- Gate questions and the human's answers (with notes) → first-class
  turns, visually distinct. (This seam is where steering input lands
  later.)
- Scope: whole-run by default; clicking a node (graph or chip) filters
  the feed to that node, with a visible breadcrumb ("feed: implement ·
  show all") so the scope is never ambiguous.
- Filters: by node, by kind (prose / tools / lifecycle). The 290KB
  events.jsonl artifact stays downloadable but is no longer the best
  reader.

### R3 — node inspector

Selecting a node (graph or feed chip) shows: status + attempts + timing
(from /state), the resolved prompt (from /nodes — including yet-to-run
nodes), the response — or, for tool nodes, the full stdout/stderr and
exit code — (from /stages once completed), and that node's feed slice. The "stage detail unavailable" state becomes impossible for
completed nodes and informative ("not started — prompt below") for
pending ones.

### R4 — graph as map

- Height-capped (~40vh), fit-to-viewport, zoom/pan.
- Restyled in place via CSS over the graphviz SVG (theme tokens, clear
  status tinting from /state, subtle active pulse). A custom renderer is
  explicitly out of scope for v3.
- Node status always painted from /state, never from replay.
- The old Events timeline remains alongside during Phase 1 rollout and
  is deleted in the final milestone once the feed has proven itself.

## Milestones

| ID | Milestone | Status |
|----|-----------|--------|
| P1 | /state + /nodes/{node} endpoints, server-derived fail-aware active set, header data included; UI untouched | todo |
| P2 | diff endpoint works for local/vm runs via the shared jj store; "no commits yet" distinguished from "no diff" | todo |
| P3 | stage outputs phone home at completion (codergen response.md; tool stdout/stderr + exit code); /stages/{node} serves them under every runner | todo |
| P4 | runner-parity e2e suite (direct vs local) over /state, /nodes, /stages (incl. tool stdout/stderr), /diff, /questions; red on pre-P1 daemon, green after P1-P3 | todo |
| R1 | header card from /state | todo |
| R2 | the feed (prose bubbles, collapsed tool rows, lifecycle dividers, gate turns, scope filter + breadcrumb) replacing the timeline as main surface | todo |
| R3 | node inspector: prompt up front, response after completion, timing/attempts, per-node feed slice | todo |
| R4 | graph height-capped/restyled/state-painted; hydrate-then-append everywhere; legacy timeline + client state accumulators deleted | todo |

Sequencing: P1 → {P2, P3} → P4 gate closes Phase 0. R1 immediately after
P1. R2 gets a visual mock approved at a human gate before
implementation. R3 after P3. R4 last, deleting the old code paths.
