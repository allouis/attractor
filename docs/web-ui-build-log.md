# Web UI build — dogfood log

Building `docs/web-ui-spec.md` (W1–W6) by running Attractor on itself via
`pipelines/build-review/pipeline.dot` (build loop with the review-core
five-lens fanout, looping implement/fix↔review until the synth verdict
reports no blocking findings).

Base: `main` (nkrnnvtv) → setup commits:
- `lvrsnpyq` docs: web-ui-spec + retarget I6 + park tui-spec
- `trozposx` build: build-review pipeline

Log of interventions (manual fixes, graph edits, re-runs, gate answers).

---

## Setup
- Wrote `pipelines/build-review/` (pipeline.dot + prompts plan/implement/fix/record).
  - Discovered `$context.<key>` errors on a missing key → review findings can't
    be interpolated into the shared implement prompt on the first pass. Resolved
    with a dedicated `fix` node reached only after a FAIL verdict, reading the
    blocking findings from `$context.stack.child.failure_reason` (manager_loop
    surfaces the child synth's failure reason into parent context). No change to
    shared review-core needed. **[design decision, not a fix during a run]**

## W1 — design foundation + nav shell
- Run 1 (webui-W1): plan (2min, correctly selected W1) → implement (11min, 3 clean
  atomic commits: showView refactor, token system+theme toggle, nav shell) → verify
  PASSED → **review_loop FAILED spuriously**: `manager_loop: graph missing
  stack.child_dotfile`. Killed the run before the fix-node cascaded on an unset
  context key.
- **INTERVENTION 1 (stale binary).** Root-caused: the installed
  `/home/agent/.nix-profile/bin/attractor` (v0.1.0) is stale — its DOT lexer drops
  the dotted `stack.child_dotfile` node attr. Proved via minimal manager_loop repro:
  old binary → "missing stack.child_dotfile"; binary freshly built from current
  source (`go build -o scratchpad/attractor-fresh ./cmd/attractor`) reads it fine.
  Fix: use the fresh binary for all `attractor run` invocations. (Current source is
  correct — parse + transforms both preserve the attr; only the installed artifact
  is old.)
- **INTERVENTION 2 (child path resolution).** manager_loop resolves
  `stack.child_dotfile` relative to the **process cwd**, not the parent dotfile dir,
  so `../review-core/pipeline.dot` missed from repo root. Fixed the build-review
  pipeline to an absolute child path; confirmed via smoke test (child pipeline runs
  to completion). Folded into the base commit so it stays out of W1's review diff.
- W1 implement work is intact and committed; only review+record remain. Re-running.
- **INTERVENTION 3 (child backend not configured).** Re-run with fresh binary got
  past manager_loop, launched review-core child, but the child's lenses/synth failed:
  `acp: no agent command configured`. Cause: the review-core child graph has no
  `acp_command` attr and the child engine (sharing the parent registry) had no
  fallback command — `--backend acp` alone doesn't set the acp backend's `Command`
  fallback. The `fix` node then ran on this bogus infra "finding" and committed an
  unverified `test(ui)` commit before I killed it; abandoned it, restored clean W1
  (3 UI commits, still todo). Fix: pass `--acp-cmd claude-agent-acp` (the backend's
  node>graph>Command fallback, per internal/backend/acp/acp.go:33-41) so the
  attr-less child nodes get the command. Applies to all future runs. **No pipeline
  edit needed.** Re-running review-record with the flag.
- Run 3 (webui-W1r2, `--acp-cmd`): all 5 lenses ran as real agents → **synth verdict
  PASS**. High-quality adversarial review: disproved the strongest "already broke"
  claim by inspection (incomplete paintNode color map is latent/unreachable, not a
  live defect), deduped 5 lenses → 8 ranked items, classified follow-ups (openRun
  nav race [pre-existing], localStorage theme validation, dockTimer leak, tab-set
  scatter) as non-blocking for a nav-shell increment. → record flipped **W1 done**.
  **W1 COMPLETE.** Commits: showView refactor, tokens+theme, nav shell, mark-done.
  - Non-blocking follow-ups noted by the review (candidates for later hardening):
    openRun race guard, whitelist localStorage theme, clearInterval(dockTimer) in
    done(), collapse the tab set into one TABS registry, dedupe dark palette via
    light-dark(). Not committed — deferred, not blocking.

## W2 — GET /workflows catalog + graph endpoint
- Full build-review, review_base=ynrnxxql. **NO INTERVENTION.** plan (W2) → implement
  (Go handlers + tests, 3 commits) → verify PASS → review-fanout **verdict PASS** →
  record. **W2 COMPLETE.** Endpoints `GET /workflows` + `/workflows/{name}/graph`
  registered. Commits: catalog, graph SVG, mark-done. First fully hands-off milestone.

## W3 — Items view
- Running full build-review, review_base=onotsslmkmrtrmsquswvvmwznmytrvwr. No intervention through W2 (verdicts PASS, no fix loops).
- Round 1 review: **verdict FAIL** (the fix-loop firing as designed — NOT my intervention;
  the pipeline handles it). Two blocking defects (correctness + prod-safety lenses):
  - **B1**: `fetchItems` swallows all non-OK/throw into `[]` → a partial source outage
    (github 500, linear ok) silently drops assigned work with zero signal; total outage
    looks like "nothing assigned". Fix: surface per-source error banner.
  - **B2**: source list hardcoded `['github','linear']` in frontend, duplicating the
    config-driven backend registry with no discovery endpoint → single-source configs
    400 every nav (compounds B1). Fix: derive sources from backend.
  → parent routed review_loop(FAIL) → `fix`. Letting the loop run (fix → verify → re-review).
- Round 2 review: **verdict FAIL** — fix over-corrected. Its fetch-guard (`itemsLoaded`
  latches true, never refetches on re-entry) removed ALL refresh → Items view freezes,
  in_progress/linked-run badges never update (spec wants ~2-3s poll). Review caught a
  regression the fix itself introduced. → round 3 (fix restores guarded polling).
  Convergent, not oscillating; visit budget 10. Still NO manual intervention.

- Round 3 review: **verdict PASS** → record. **W3 COMPLETE** in 3 review rounds.
  Fix loop delivered real value: new GET /items/sources discovery endpoint (B2),
  per-source failure banner (B1), URL scheme-check + rel=noopener (security), and
  fixed the re-entry-refetch regression it caught itself in round 2. 7 commits.
  Non-blocking follow-ups left: N1 (empty-config vs nothing-assigned msg), N2
  (dropdown rebuild without refetch), continuous 2-3s poll (W5-coupled).

## W4 — Items run action
- Running full build-review, review_base=snluszovkyqyznrqxwunuqtpxmsmvpwp.
- Round 1 review: **verdict FAIL** (2 blocking). (1) Poisoned workflow cache —
  fetchWorkflows caches [] on failure (truthy → never retries) → one /workflows blip
  kills the picker for the session (same failure-caching class as W3 B1). (2) The new
  JS tests are strings.Contains greps that pass even if the code is deleted (comments
  match) — assert existence not behaviour; reviewer wants a Playwright drive or deletion.
  → fix loop (no manual intervention). RECURRING THEMES across milestones: caching of
  failed/empty fetches, and string-match tests being worthless for JS behaviour.
- Round 2 review: **verdict PASS** → record. **W4 COMPLETE** in 2 review rounds.
  Fixes: workflow-cache no longer poisoned by transient failure, double-submit guard,
  read-only PR repo field, and dropped the comment-spoofable grep tests (chose deletion
  over Playwright). 6 commits.

## W5 — Runs view
- Running full build-review, review_base=nnwlkxstyxuunutsvwtsoxzpxsukvyyu.
- **Round 1 verdict PASS** (no fix loop). **W5 COMPLETE.** Runs fleet view: poll-live
  counts + needs-human flag (added needs_human to server run summary), cancel-with-confirm,
  workflow+item provenance on rows. 5 commits.

## W6 — Workflows view (final)
- Running full build-review, review_base=opzqkuyyxsqlwzomqxvrlspzxqtuvoxt.
- **Round 1 verdict PASS** (no fix loop). **W6 COMPLETE.** Workflows view (catalog list +
  static graph) + run→workflow/item backlinks across views. Server stamps workflow_name on
  catalog-dispatched runs. Non-blocking follow-ups noted by review: gate workflow_name stamp
  on catalog membership, Introduce Parameter Object for run provenance (ItemRef+WorkflowName),
  add workflow_name reload-persistence test.

## Post-build cleanup + verification
- **INTERVENTION 4 (stray status.json).** The review-core `synth` node writes its status.json
  into the run cwd (repo root), not just its stage dir, so jj snapshotted it into commits from
  W5 on. **[attractor bug worth reporting.]** Purged from history (squashed the deletion into the
  origin commit vkrvktvtxsrz), added to .gitignore. All commits + working copy clean.
- **FINAL GATE:** `go test ./... -race` ✓ all green, gofmt ✓ clean, `nix build .#attractor` ✓.
- **END-TO-END (real browser via Playwright):** served the built binary, drove /ui —
  default Items landing ✓, 3-tab nav+routing ✓, W3 per-source failure banner live ("could not
  load: github…") ✓, Workflows catalog → static graph renders (bug-fix DAG, server SVG) ✓,
  workflow→item backlink ✓, dark theme toggle ✓. Only console errors = favicon 404 + expected
  github 502 (gh unauthed), handled gracefully by the banner — NO JS crashes.

## Summary
- 6/6 milestones done, ~34 commits. Manual interventions: 3 upfront infra (stale binary,
  manager_loop child path, --acp-cmd for child backend) + 1 cleanup (stray status.json).
- Zero interventions in the actual milestone *content* — plan/implement/review/fix/record ran
  autonomously. The review-fanout loop blocked merges on real defects (W3: silent source-failure
  + hardcoded sources + a regression the fix introduced; W4: poisoned workflow cache + worthless
  tests) and iterated to clean. W1/W2/W5/W6 passed review first-or-second try.
- Recurring review themes (candidate systemic hardening): caching failed/empty fetches; string-
  match UI tests that don't exercise JS (a real JS test harness would help).
