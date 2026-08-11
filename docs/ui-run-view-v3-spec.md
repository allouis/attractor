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
5. Clicking a node shows "stage detail unavailable" throughout a live
   VM run: stage files sync to the daemon only at the run's END (the
   terminal-event UploadDir sweep), so mid-run — when the operator is
   actually watching, e.g. at a plan gate — `/stages/{node}` 404s.
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

### P3 — stage outputs sync per stage, not at run end

Stage phone-home already exists — but only at the run's END: report mode
uploads the whole stage tree right before forwarding the terminal event
(cli.go runEngineReporting, `UploadDir`). So a FINISHED VM run has full
stage files daemon-side (codergen `response.md`, tool
`stdout.txt`/`stderr.txt`, per-stage `status.json`), while a LIVE one
has nothing — which is exactly when the operator is looking (the plan
gate had nothing to show mid-run), and a crashed/killed VM loses the lot.

P3 moves the sync to **each `stage_completed`**: upload that stage's dir
incrementally (the terminal-event upload stays as the catch-all sweep).
`GET /stages/{node}` then works for every completed stage of every
runner while the run is live.

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

### P5 — live stage tails

Watch a check's stdout grow while it runs, from the browser, under every
runner. Four steps, in order — each is independently useful:

1. **Manifest ownership split** (promoted from the codebase-review
   backlog, persistence design C): the engine's identity record becomes
   `run.json`; the daemon keeps `manifest.json`; JSON field names
   unchanged so existing dirs load. After this every run-dir path has
   exactly ONE writer — the property that makes sharing a run dir safe
   at all (today the two manifest.json writers clobber each other on the
   direct runner, and only guest-local disks protect VM runs).
2. **tool.go streams to disk**: stdout/stderr go through
   `io.MultiWriter(stage file, buffer)` so the files grow while the
   command runs. Today they are written only after the command exits, so
   no transport can tail a live check. Benefits every runner.
3. **VM transport**: mount the child's logs root into the guest over rw
   9p, host side under the daemon's run dir. Safe by construction after
   step 1 (single writer per path); stage files carry no SQLite/mmap
   load, so the G1 rw-9p pivot does not apply — but write-visibility
   latency under 9p cache modes must be verified empirically, the same
   way G1 was. direct/local runs already have host-visible files.
4. **Tail endpoint**: `GET /pipelines/{id}/stages/{node}/tail?file=stdout&offset=N`
   (long-poll or SSE) serving incremental reads; the feed's check blocks
   (R2) upgrade from appears-when-done to live, ANSI-rendered.

Explicitly rejected: streaming output through the event channel —
multi-MB test logs would bloat events.jsonl and chunk the verbatim
record; files stay files, the tail reads them.

## Phase 1 — the run view, rebuilt

Built strictly on Phase 0 data. The existing run view is replaced, not
patched — its client-side state accumulators are deleted.

### R1 — header card

Repo, issue (identifier + title, linked to the tracker url), workflow,
runner + image, status pill, elapsed time (live), run id. All from
/state. One glance answers "what is this run". The card is also the
template for each row of the runs INDEX view (identifier · title ·
status · repo · elapsed) — adopted there during R4's cleanup pass.

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
- **Markdown**: agent prose renders as markdown (safe subset — headings,
  emphasis, inline/block code, lists, links; HTML-escape first, no raw
  HTML pass-through). Same renderer serves the gate plan panel.
- **Collapse, never truncate**: long content clamps to a preview with an
  expand control — prose bubbles ~12 lines, check outputs show the tail
  (head+tail for huge logs), the gate's plan snippet expands to the full
  plan in place. Full text is always reachable; nothing is cut.
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
| P1 | /state + /nodes/{node} endpoints, server-derived fail-aware active set, header data included; UI untouched | done |
| P2 | diff endpoint works for local/vm runs via the shared jj store; "no commits yet" distinguished from "no diff" | done |
| P3 | stage dirs upload at each stage_completed (terminal sweep stays as catch-all); /stages/{node} serves completed stages of LIVE runs under every runner | done |
| P4 | runner-parity e2e suite (direct vs local) over /state, /nodes, /stages (incl. tool stdout/stderr), /diff, /questions; red on pre-P1 daemon, green after P1-P3 | done |
| P5a | manifest ownership split: engine writes run.json, daemon owns manifest.json; direct-runner clobber window closed; reload tolerant of both layouts | done |
| P5b | tool handler streams stdout/stderr to stage files while the command runs | done |
| P5c | VM child logs root shared rw into the run dir (single-writer safe post-P5a); 9p write-visibility verified empirically | done |
| P5d | stages tail endpoint (offset reads, long-poll/SSE); feed check blocks render live | done |
| R1 | header card from /state | todo |
| R2 | the feed (prose bubbles, collapsed tool rows, lifecycle dividers, gate turns, scope filter + breadcrumb) replacing the timeline as main surface | todo |
| R3 | node inspector: prompt up front, response after completion, timing/attempts, per-node feed slice | todo |
| R4 | graph height-capped/restyled/state-painted; hydrate-then-append everywhere; legacy timeline + client state accumulators deleted | todo |

Sequencing: P1 → {P2, P3} → P4 gate closes Phase 0. P5a-P5d follow in
order (P5a is independently valuable and can land any time; P5c waits
for P5a; P5d waits for P5b+P5c) — P5 is Phase 0.5, not a Phase 1
blocker. R1 immediately after P1. R2 gets a visual mock approved at a
human gate before implementation; its live-tail upgrade arrives with
P5d. R3 after P3. R4 last, deleting the old code paths.
