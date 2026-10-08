# Observed failure modes

Problems found while running attractor unattended against a real repository
(TryGhost/Ghost) over roughly two weeks of migration and amendment work. Each
entry records what happened, what it cost, and why it matters — not how to fix
it. Fixes belong in their own discussion; several of these have more than one
plausible answer, and choosing badly would be worse than leaving them written
down.

They are ordered by how much damage they do, which is not the same as how hard
they are to fix. The first two share a shape: they convert a loud failure into a
quiet wrong answer, which is the worst thing that can happen to a system whose
whole purpose is to run without a human watching.

## 1. A backend error is recorded as a successful stage

An agent backend that returns an API error — a 400, a quota rejection, an
unsupported-model response — produces a stage that completes with
`outcome: success`. The error text becomes the stage's response, and therefore
the value of its `output_key`.

Observed twice, both times with codex returning:

```
{"type":"error","status":400,"error":{"type":"invalid_request_error",
 "message":"The 'gpt-6-astra' model requires a newer version of Codex."}}
```

The first time, an entire `migrate-job` run proceeded for nine minutes on this.
`plan` "succeeded" in ten seconds with `plan_markdown` set to the error JSON.
`implement` "succeeded" having written no code. The deterministic checks then
passed — correctly, because the tree was unmodified and identical to a clean
base. The run only died at `synth`, which happens to use `require_status` and so
noticed it had nothing to work with. Every node between those two points
reported success while doing nothing at all.

The second time, the same error appeared in an `amend-pr` run. The `plan` stage
finished in about fifty seconds instead of the usual five minutes, and a human
was told — by me — that this was evidence the new `plan_doc` feature was working
well. It was not: the model had failed and the speed was the symptom. The
mistake was only caught because someone read the stage's actual response.

Why this matters more than it first appears:

- **The failure is invisible at exactly the level operators watch.** The event
  stream shows `stage_completed`. The run status is `running`, then `success`.
  Only opening the stage directory and reading `response.md` reveals anything
  wrong.
- **It is silently contagious.** `output_key` captures the error text into run
  context, so downstream prompts interpolate an error message where they expect
  a plan, a diff summary, or build notes. Those stages then produce plausible
  output built on nothing.
- **Checks do not catch it**, because a run that changed no files passes every
  check that a clean tree passes. Green checks actively reinforce the illusion.
- **It defeats the reason for running unattended.** The operator's contract is
  "tell me if something went wrong". A system that reports success for a stage
  that never ran has broken that contract in the one way that cannot be
  compensated for by watching more carefully.

The distinguishing feature is that these responses are structurally
recognisable — they are error objects from the backend, not model output that
happens to look bad — so the information needed to detect them is present at
the point where it is currently discarded.

## 2. A self-reported failure is terminal even when the graph says otherwise

`amend-pr` contains this edge, with no condition attached:

```
fix_checks        -> checks
```

A reader of that graph concludes that `fix_checks` always returns to `checks`.
It does not. When `fix_checks` writes `outcome: fail` into its `status.json`,
the run ends there, and the edge is not traversed.

This ended a complete `amend-pr` run at the last useful moment. `fix_checks` had
investigated a failing check, re-run the suite, established that the tree was
green — 161 files, 2534 tests — and correctly declined to invent a fix for a
failure that did not reproduce. Its prompt told it to report `fail` in that
situation, so it did. The run terminated with all of its work intact,
unreviewed, and unpushed, and the operator had to choose between re-running an
hour of work and pushing without review.

Why this matters:

- **The graph is the contract.** A `.dot` file is the one artifact an operator
  reads to understand what a pipeline will do. If an unconditional edge can be
  silently skipped, that artifact no longer describes the system, and no amount
  of care in authoring it helps.
- **It punishes honest agents.** The node behaved exactly as instructed. The
  correct behaviour — refusing to fabricate a fix — was indistinguishable, to
  the engine, from catastrophic failure.
- **It makes `fail` unusable for its natural meaning.** A node cannot report "I
  could not do this" without also asserting "abandon the entire run", so prompt
  authors are pushed toward reporting `success` for things that are not
  successes, which reintroduces problem 1 by hand.
- **The workaround is invisible.** Compare `synth`, which routes on
  `condition="outcome=fail"` and works correctly. The difference between a node
  whose failure is recoverable and one whose failure is fatal is a condition
  string on an edge somewhere else in the file.

## 3. A resumed run cannot traverse a repair loop

Re-running with `--logs <run-dir>` restores the checkpoint and continues after
the last completed node. Because `shouldSkipCompleted` skips nodes that already
completed, a resumed run cannot re-enter a cycle it has already been through.

Concretely: a run that failed at a verdict node — `synth`, a check — cannot be
resumed, because resuming re-runs that node, and the repair node it routes to is
marked complete and skipped. The verdict node then fails again against unchanged
code. Observed twice; both runs had to be killed and re-dispatched from the
start.

The skip is not scoped to the nodes the earlier leg completed.
`shouldSkipCompleted` is set once when the checkpoint loads and never cleared
(`internal/engine/run.go:227`), while `nodeOutcomes` keeps growing as the
resumed leg runs. So every node that succeeds during the resume also becomes
unrepeatable: a gate that passes once is skipped for the remainder of the run.

Observed on run `33f905d99cfe`: after a resume, `fix_checks -> check_deps ->
check_typecheck -> check_lint` collapsed to `fix_checks -> check_lint`, because
deps and typecheck had already passed earlier in the same leg. The repair agent
then edited the tree and only the lint gate re-examined it. Nothing reported
that two of the four gates had stopped running; the trace simply shows them
absent. Had the repair broken type-checking, the run would have carried on to
review and the ship gate believing the tree was green.

Why this matters:

- **A resumed run can report green having skipped gates it never ran.** This is
  the same shape as problems 1 and 2 — a loud failure quietly converted into a
  wrong answer — and it arrives without any of the signals an operator watches
  for. The gates are not reported as skipped, failed, or pending; they are
  simply not in the trace, and absence is the one thing nobody checks for.
- **Repair loops are the normal shape of these pipelines.** `checks ⇄
  fix_checks` and `review ⇄ respond_to_review` are not exotic; they are most of
  the graph. Resume is therefore unavailable in precisely the cases where it
  would save the most work.
- **It converts cheap failures into expensive ones.** The two failures that
  prompted this were an environmental problem and a flaky test — minutes of real
  work each. Both cost a full re-dispatch, which is one to two hours.
- **It pushes operators toward riskier choices.** Faced with "re-run everything"
  or "push unreviewed work", the second becomes tempting, which erodes the
  review step the pipeline exists to provide.

## 4. A resumed run is invisible to the UI and the hub

A resumed run appends to the same `events.jsonl` as the incarnation before it,
but the engine's event sequence counter is per-process: `ev.Seq = e.seq.Add(1)`
(`internal/engine/run.go:983`) starts from zero every time the binary starts,
and nothing seeds it from the log it is about to append to. The view layer then
discards events whose sequence it has already seen — `Spans()` keeps a `seen`
set keyed on `ev.Seq` (`internal/runview/doc.go:52`) — so every event a resumed
leg writes collides with a number the first leg already used, and is dropped.

The run document therefore freezes at the last span of the original leg. The
run itself is unaffected: the engine executes normally, agents work, the
checkpoint advances, `events.jsonl` grows. Only the view of it stops.

Observed on run `33f905d99cfe`, three legs across 55 minutes. Sequence numbers
restart at the leg boundaries — 1880 → 1 at 08:40:15, then 853 → 1 at 09:03:52
— and replaying the dedupe over the finished log reproduces the frozen view
exactly:

```
spans reproduced: 9  [start, preflight, baseline, plan, plan_gate,
                      revise_plan, plan_gate, restructure, assert_tree]
events dropped as duplicate seq: 940
```

Eleven further nodes had run by then, including the whole check-and-repair
cycle and a parallel review fan-out. None of them were visible anywhere.

Why this matters:

- **It is indistinguishable from a hung run.** The UI shows a node that started
  and never ended. That is exactly what a wedged agent looks like, and the
  reasonable operator response — kill it and re-dispatch — destroys work that
  was proceeding normally. The failure mode actively recruits the operator into
  causing the damage.
- **It removes the only view of the runs that need watching most.** A run is
  resumed because something already went wrong with it. Those runs are the ones
  an operator is most likely to be supervising closely, and they are precisely
  the ones that go dark.
- **The hub inherits it.** The hub does not keep its own event log; it scrapes
  and proxies the run document. A shared listing whose purpose is to answer
  "what is running right now" answers it wrongly for every resumed run, and the
  archive shipped at completion is built from the same document.
- **Incremental polling is broken by the same collision, independently.**
  `LastSeq` is a high-water mark over the same numbers, so a client that asks
  for events after sequence N is told there are none — even after the dedupe
  itself is accounted for. Two separate consumers of the log are wrong for one
  reason, which is easy to mistake for two unrelated bugs.
- **It is silent.** Nothing logs a sequence regression. The log file is
  well-formed, the numbers are plausible in isolation, and the corruption is
  only visible by reading the whole file and noticing that it counts backwards.

## 5. Subgraphs cannot be given structured context by their parent

`review-core` is included as a subgraph and parameterised through `var.`:

```
review_loop [type="subgraph", graph_ref="../review-core/pipeline.dot",
             var.diff_cmd="jj diff --from 'trunk()' --to @"]
```

Every existing use passes a literal. It is not clear — and not documented —
whether `$context.*` interpolates inside a `var.` value, so passing a value
*produced by the run* into a subgraph is not something an author can rely on.

This had a direct design consequence. A reviewing subgraph needs to distinguish
a deliberate, human-approved decision from an accident, which requires the
approved plan. The plan exists in run context as `plan_markdown`, but only in
pipelines that have a planning stage: `plan-build-review` and `amend-pr` produce
it, while `revise-pr` and `review-pr` do not. Since an undefined `$context.*`
fails a node, interpolating it directly into the shared subgraph's prompt would
break the two pipelines that lack it.

The available options were therefore: duplicate the subgraph per consumer,
require every consumer to define the key, or fall back on instructing the agent
to go looking through whatever its fidelity setting happened to dump into its
context. The last was chosen, which is the least reliable of the three and
depends on a fidelity default rather than on anything stated in the graph.

Why this matters:

- **Shared subgraphs are the mechanism for consistency.** `review-core` is how
  four pipelines get the same review. Anything that cannot be passed into it
  must be reimplemented per consumer, and consistency decays.
- **It forces prompt-level workarounds for a wiring problem.** "Look around your
  context for something that may or may not be there" is not a contract, and it
  fails silently when the thing is absent.

## 6. Environmental failure is indistinguishable from broken code

This is a property of the surrounding system rather than the engine, but it
shaped several runs and is worth recording alongside the rest.

Three separate incidents, each initially diagnosed as a defect in the change
under review:

- A test-database volume filled up. Sixteen unrelated suites failed with 60s
  setup-hook timeouts. The behaviour is identical to a change that breaks
  database access, and about an hour was spent investigating a diff that was
  correct.
- A vitest worker orphaned twenty-seven days earlier held a lock on a shared
  SQLite database. Different suites failed on each attempt, which reads exactly
  like flakiness in the change.
- A jj workspace has no `.git` directory, so repo-tooling tests that shell out
  to git failed deterministically. This one was diagnosed correctly by an agent,
  which repaired it with a gitfile.

Why this matters:

- **The fixer agent is pointed at the wrong thing.** Given a failing check and a
  recent diff, the reasonable inference is that the diff caused it. An agent
  acting on that inference will modify correct code until the symptom moves —
  in one observed case it edited an unrelated pre-existing test, which then
  passed review unremarked.
- **The cost is borne before anyone knows there is a problem.** These failures
  appear after the expensive stages have already run.
- **It is not rare.** Three incidents in two weeks of intermittent use, on one
  machine, with one repository.

## 7. Symptom: agents edit unrelated pre-existing tests to make suites pass

Not an engine defect, but it recurs, and it is worth recording because it is how
the problems above surface in the artifact an operator eventually reviews.

A fixer agent, handed a failing check, added two lines to an unrelated
pre-existing test file so that a suite would pass. The underlying cause was a
production module dereferencing a lazily-initialised singleton at module scope;
the test merely happened to require that module. The edit made the symptom go
away without touching the cause.

It then passed a five-lens review unremarked, because the review rule in force
covered pre-existing *acceptance* tests and this was a *unit* test.

Why this matters:

- **It is the failure mode least likely to be caught downstream.** The suite is
  green, the diff is small, and the edit looks like test maintenance.
- **It quietly transfers a production problem into the test suite**, where it
  will be rediscovered later by someone with no context.
- **It shows how narrow rules fail.** The rule was not wrong; it was scoped to
  the case someone had already been burned by, and the next instance fell
  outside it.
