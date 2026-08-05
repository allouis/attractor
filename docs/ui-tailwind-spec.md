# UI: mobile-responsive + graph restyle (Tailwind)

Make the web UI usable on a phone and restyle the run graph to match the UI.
The Tailwind toolchain is already wired (T0, done): a tree-shaken stylesheet is
compiled from `internal/server/ui/index.html` and inlined into the served page;
utilities map onto the existing `--` design tokens so they are theme-aware, and
Preflight is OFF so Tailwind is additive over the existing hand CSS.

Survey (agent-browser, iPhone 390px): the shell, Items, filters and the run
graph already fit; the breakages are the **Config repos/providers tables** and
the **Runs table** (overflow / clipped columns), plus header polish and code
blocks. The graph is graphviz `dot -Tsvg` (inline SVG) with hardcoded
`black`/`white` — ugly and off-theme.

## Working rules for each milestone
- Use Tailwind utility classes (config in `ui/tailwind.config.js`); prefer the
  token-mapped colours (`bg-surface-1`, `text-muted`, `border`, `text-state-*`)
  so light/dark keeps working — never hardcode hex.
- Preflight is off: when you fully convert a block you may add targeted resets.
- **After editing classes in `index.html`, regenerate the stylesheet and commit
  it:** `cd internal/server/ui && tailwindcss -c tailwind.config.js -i input.css
  -o tailwind.css --minify` (nix build also regenerates it authoritatively via
  preBuild, but keep the committed copy fresh so `go test`/dev match).
- Acceptance for every UI milestone: **no horizontal overflow at 390px**
  (`document.documentElement.scrollWidth <= innerWidth`) and the view is usable;
  existing desktop layout not regressed. Gate: `go test ./... -race`, gofmt,
  `nix build .#attractor` + `.#vm-runner`.

## Milestones

| # | Deliverable | Status |
|---|---|---|
| T1 | **Shell/header responsive.** Header + nav (`<header><nav>…`) to Tailwind; on ≤640px the nav collapses (wrap or a menu) and the theme toggle stays reachable; fix the sticky-header tap-intercept (taps on nav must land, not hit the header). No overflow at 390px. | done |
| T2 | **Config tables → cards.** The Repos and Providers editable tables overflow to ~572px. On ≤640px render each row as a stacked card (label/value per line) using Tailwind; keep the desktop table at ≥640px. Inputs full-width + tappable on mobile. | done |
| T3 | **Runs table responsive.** The run-list table clips the `by` column. On ≤640px show a stacked/card row (run id, workflow, repo, status, when) with no clipped columns; desktop table unchanged. | done |
| T4 | **Graph restyle + engine selector.** (a) Restyle the inline run-graph SVG with tokens: edges use `--border`/`--text-muted` with clean stroke weight + rounded caps (no black), nodes use `--surface-1`/`--border`, text uses `--text` + the UI font — theme-aware. Put the rules in `ui/input.css`. (b) Add a graphviz **engine selector**: `render.SVG` takes an engine, `runDot` passes `-K<engine>`; the daemon graph endpoint accepts `?engine=` (allowlist: dot, neato, fdp, sfdp, circo, twopi); the run-detail view gets a small dropdown that re-renders the graph with the chosen engine. Default stays `dot`. | done |
| T5 | **Polish.** Code/prompt/response/diff blocks in the stage inspector get `overflow-x:auto` (no page overflow); touch targets ≥40px for controls; filter rows wrap cleanly. Drop in provided Tailwind UI components where they fit. | done |

## T6 — integrate Tailwind Plus components

Now on **Tailwind v4**, so Tailwind Plus v4 markup is drop-in. Curated component
HTML is provided under `docs/tailwind-components/` (extracted from Tailwind Plus,
the operator's license). The vendored interactivity lib is at
`internal/server/ui/elements.js` (`@tailwindplus/elements` v1.0.22 — powers the
`el-dropdown`/`el-disclosure`/`el-menu` custom elements used by the shell/nav).

**Rules for every T6 milestone:**
- The provided component HTML uses stock `gray-*`/`indigo-*` colours — **remap
  them to the UI tokens** (`bg-surface-1`, `text-ink`/`text-muted`,
  `border-line`, `text-accent`, `text-state-*`) so light/dark keeps working.
  Never leave hardcoded gray/indigo.
- Keep the existing behaviour (hash routing, SSE, run-form submit, filters).
- Acceptance unchanged: **no horizontal overflow at 390px**, desktop not
  regressed, gate green (`go test -race`, gofmt, `nix build .#attractor` +
  `.#vm-runner`). Regenerate `ui/tailwind.css` after class changes.

| # | Deliverable | Status |
|---|---|---|
| T6a | **Vendor + wire `@tailwindplus/elements`, then adopt badges + empty-states.** Add `//go:embed ui/elements.js` and inline it as a `<script type="module">` (mirror the tailwind inject) so `el-*` elements upgrade. Replace status pills with the `docs/tailwind-components/badges.html` badge (token-remapped, one per run state) and the "no items"/"no runs" states with `empty-state.html`. Verify a custom element upgrades (e.g. an `el-dropdown` test) + pills/empty-states render theme-aware. | done |
| T6b | **Adopt the Tailwind Plus table** (`table.html`) for the Runs + Config lists — token-remapped, preserving the ≤640px stacked-card behaviour from T2/T3. | done |
| T6c | **Adopt the form layout** (`form-layout.html`) for the Config panels + the run-form modal — token-remapped, full-width tappable inputs on mobile. | done |
| T6d | **App-shell / nav swap** (`app-shell-stacked.html`) — replace the header/nav with the stacked shell + a real mobile menu via `el-disclosure`. Most invasive: keep hash routing + the theme toggle wired. Do last. | done |

## T7 — polish pass (graph aesthetics + compact Config lists)

Same rules as T6 (token colours only, no overflow at 390px, gate green). These
are quality passes — the `ui_review` node must **look at the result and iterate**,
not just check overflow.

| # | Deliverable | Status |
|---|---|---|
| T7a | **Prettier run graph.** The graph is themed but still looks utilitarian/ugly. Make it genuinely nice and on-brand: rounded node corners with soft token-based fills + good light **and** dark contrast; cleaner edges (smoother splines + right-sized arrowheads, no heavy black); the UI font at a comfortable size; tidy node/rank spacing. Tune BOTH the DOT attributes in `internal/render/render.go` (node `style`/`shape`/`fontname`/`fontsize`, edge `splines`/`arrowsize`, graph `nodesep`/`ranksep`) AND the SVG CSS in `ui/input.css`. Keep the status colours meaningful but harmonious. Default engine stays `dot`. `ui_review` MUST open a run detail, view the graph in light + dark, and iterate until it looks polished. | done |
| T7b | **Compact Config repos/providers.** The mobile stacked-card layout shows every field per repo, so it gets very long with many repos. Make each repo (and each provider) a **collapsible** item: a compact summary row (owner/name + a small runner/backend chip) that expands to the editable fields on tap (accordion — vanilla-JS toggle like the nav menu, no new dep; ARIA-expanded). Collapsed by default. Keep the desktop table unchanged. No overflow at 390px; add/remove repo + save still work. | done |

## T8 — runs-list density + run-detail findings presentation

| # | Deliverable | Status |
|---|---|---|
| T8a | **Compact, glanceable runs list on mobile.** The current stacked-card run rows show every field, so the list is huge and hard to scan. On ≤640px render each run as a **tight one/two-line summary** — short id + workflow + a status **badge** + relative time — the whole row tappable through to the detail; drop the per-field label stack (extra fields live in the detail view, not every row). Keep the desktop table. No overflow at 390px; filters + live updates still work. | done |
| T8b | **Run-detail: stop rendering review findings as a scary error.** On a run detail the review/verdict text (e.g. `stack.child.failure_reason`) is shown as a big unlabeled red error banner even when the run **succeeded** (the findings were resolved by the fix loop) — very confusing. Find where the detail view surfaces this and present it correctly: a clearly-labelled, collapsible **"Review notes"** section (not error-red), and on a `success` run either hide resolved findings or mark them clearly resolved. A genuine run failure must still show its real error, distinctly. Verify on run `8f33e2e1c460a78c` (succeeded) and on a failed run. | done |

## T9 — generalise the run view (stop assuming `build-review`)

The run-detail view has `build-review`-specific assumptions and a restart bug.
Make it describe *any* run. Each milestone dogfooded via `ui-build-review`.

| # | Deliverable | Status |
|---|---|---|
| T9a | **Finished-run state survives a daemon restart.** Graph node status is painted from the daemon's *in-memory* event history (`registry.history`), replayed over SSE on open. After a daemon restart that memory is gone, so an old completed run repaints nothing → nodes stale as "executing". Persist/replay from the on-disk `events.jsonl`: when a run isn't in memory (or always), reconstruct its history from `events.jsonl` on Subscribe so the graph/timeline/terminal node states are correct. Acceptance: complete a run, restart the daemon, open it → every node terminal (completed/failed), nothing "executing"; timeline intact. Keep live runs unchanged. | todo |
| T9b | **Drop the dedicated "Review notes" section — attribute nested findings to their node.** "Review notes" bakes `build-review` semantics into the generic view. A `stack.manager_loop` node's forwarded child-pipeline verdict/findings are really *that node's* output — surface them in the **node inspector** (click the node → see its child's verdict/findings), and remove the special top-level Review-notes section + the `classifyPipelineFailed` "review" case. The run's OWN failure stays the red banner. Acceptance: a `build-review` run shows its review findings under the `review_loop` node's inspector; no dedicated section; a workflow with no child pipeline shows no empty leftover. | todo |
| T9c | **Derive the run's produced diff from jj (not an uploaded artifact).** web-ui-v2 U4 wants "the run's produced change", but the panel only reads a `*.diff` artifact, which self-dev pipelines never upload → always empty. Record the run's jj revision range — the repo `@` change-id at run start (base) and at end (tip) — on the run, and serve `jj diff --from <base> --to <tip>` for the diff panel; keep the artifact path as a fallback. Applies to runs whose cwd is a host jj repo (direct runner); VM runs stay "no diff" until results-export (vm-workspace G2). Acceptance: a `build-review` run's Diff panel shows the milestone's real change. | todo |
