# Multi-lens review — spec

Replace the single-agent `review` stage with a **shared multi-lens
review sub-pipeline** (`review-core.dot`): several adversarial reviewers
run in parallel, each from a different lens, and a `synth` agent merges
their findings into one review plus a PASS/FAIL verdict. It's reused by
the **PR review** pipeline and the **implement** pipeline's self-review,
via the `stack.manager_loop` inline sub-pipeline we already have.

Status: **ready to build** (milestone ledger below).

## Design

### `review-core.dot` — the shared graph

```
start → fan_out[parallel] → correctness  ─┐
                          → design        │
                          → prod_safety   ├→ synth → done
                          → simplification │
                          → tests         ─┘
```

- **5 lens agents**, each a codergen node with a short **inline**
  adversarial prompt, uniform model (v1). Each runs `$context.diff_cmd`
  to see the change and reviews it through one lens:
  - **correctness** — "This code is broken. Find the bugs — edge cases,
    error paths, races."
  - **design** — "Review from a clean-code perspective; focus on the
    coupling and cohesion of the modules."
  - **prod_safety** — "Why should we *not* merge this to production? How
    does it break? Review adversarially."
  - **simplification** — "How can we simplify this and make it less
    complected?"
  - **tests** — "Do the tests still pass if we refactor the
    implementation? Are they coupled to implementation detail?"
- **Findings flow via context.** Each lens sets `output_key=review.<lens>`
  (new attr, RV1) so its **full** response is captured into its outcome
  `context_updates` — which `parallel` bundles into `parallel.results`.
- **`synth`** is the convergence node (skip `fan_in`, which only picks a
  winner). Its prompt interpolates `$context.parallel.results`, merges
  the five findings into one review, and **self-reports a verdict** via
  the status-file contract: outcome `SUCCESS` + `context_updates
  {review.summary, review.verdict=pass}` when nothing blocks, else
  `FAIL` + the blocking issues. So `review-core`'s final outcome *is* the
  verdict — a parent can branch on it.
- **`diff_cmd`** is the one input (`vars="diff_cmd"`): the command that
  shows the change under review. Different callers pass different diffs
  (below). Standalone: `attractor run review-core -var diff_cmd="jj diff"`.

### Reuse via `stack.manager_loop`

Both parents run `review-core` inline as a child, seeding `diff_cmd`
through the manager-loop node's `stack.child.var.diff_cmd` override
(seeds the child's initial context, C6):

- **`review.dot`** (PR review): `checkout → review_loop → done`, where
  `review_loop` is `stack.manager_loop`, `stack.child_dotfile=review-core.dot`,
  `stack.child.var.diff_cmd="gh pr diff $context.pr_number --repo $context.repo"`.
  The single-agent `review` node is removed.
- **`implement.dot`** (self-review gate): `implement → review_loop →
  [FAIL: implement | PASS: done]`, `stack.child.var.diff_cmd="jj diff"`.
  A failing review routes back to `implement` (bounded by
  `max_node_visits`); PASS exits. The agent reviews its *own* uncommitted
  work before the pipeline is allowed to finish.

### Why context, not files

Lens findings ride **context** (`output_key` → `context_updates` →
`parallel.results` → `synth` interpolates `$context.parallel.results`).
Nothing is written into the repo `cwd`, so the diff under review — and
`implement`'s working tree — stay clean. This is the payoff of the
context-interpolation migration: it works under all fidelities and needs
no out-of-repo scratch dir.

## The one new primitive

`output_key` (codergen node attr): when set, the node's **full** response
text is written to `context_updates[<output_key>]`, in addition to the
existing truncated `last_response`. General-purpose (any node can expose
its output downstream); the review lenses are the first user. Inside a
`parallel` branch it lands in that branch's entry in `parallel.results`.

## Milestone ledger

| # | Milestone | Deps | Status |
|---|---|---|---|
| RV1 | `output_key` codergen attr — full response → `context_updates[output_key]` (works in `parallel` branches). | — | todo |
| RV2 | `review-core.dot` + inline lens prompts + `synth`; standalone-runnable (`-var diff_cmd=…`); lints clean; a fixture run with a fake backend proves parallel fan-out → `synth` reads `parallel.results` → verdict. | RV1 | todo |
| RV3 | `review.dot` runs `review-core` via `stack.manager_loop` (`diff_cmd = gh pr diff …`); drop the single `review` node; e2e proves a PR routes through the lenses. | RV2 | todo |
| RV4 | `implement.dot` self-review gate — `manager_loop(review-core, diff_cmd="jj diff")` → FAIL re-implements, PASS exits. | RV2 | todo |
| RV5 | Docs — this spec's status; note `output_key` in attractor-spec node-attr table; update `review`/`implement` header comments. | RV3, RV4 | todo |

## Testing conventions

- **RV1**: a codergen node with `output_key="foo"` and a fake backend
  returning "hello" exposes `context.foo == "hello"` (full, untruncated);
  inside a `parallel` branch it appears in `parallel.results`.
- **RV2**: a `review-core` run with a fake backend runs all five lenses
  concurrently and `synth` sees each lens's finding via
  `$context.parallel.results`; a lens finding is present in `synth`'s
  prompt.md. Lint passes.
- **RV3**: `review.dot` (fake backend) checks out then runs the five
  lenses inline via `manager_loop`; the single-agent path is gone.
- **RV4**: `implement.dot` with a `synth` verdict of FAIL routes back to
  `implement`; PASS reaches `done`.

## Not in scope (later)

- Per-lens model tuning (v1 uniform; split cheap/strong once baselined).
- A 6th security lens gated to risky surfaces.
- Posting the synthesized review back to the PR (deliverable is the run
  output for now).
