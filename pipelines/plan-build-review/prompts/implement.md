Implement the change the brief below asks for. The repo is checked out in
your working directory.

<brief>
$context.brief
</brief>

A planning stage already ran and a human approved its plan — follow it,
including its test seams and slices; if reality forces a deviation, note
it in your response. The approved plan:

---
$context.plan_markdown
---

Reviewer feedback the human left when approving the plan (empty if they
approved without a note — treat any text here as an instruction to honour
while implementing):

---
$context.human.note
---

Read the relevant existing code before you touch anything — prefer
extending existing patterns over inventing new ones. Then work one
vertical slice at a time, red/green:

1. **Red.** Write one failing test for the slice, at the seam the plan
   names. Run the focused test and watch it fail — for the right reason
   (the behaviour is missing, not a typo or a bad import). A test that
   passes on its first run is pinning something that already works: fix
   the test.
2. **Green.** Write the minimum code to make that test pass. Nothing
   speculative, nothing beyond the slice.
3. **Commit** the slice with `jj` (never `git`): small, atomic, message
   per the repo's conventions. Then take the next slice.

For a bug, the first slice reproduces it: a test that fails on the
current code because of the bug, then the fix that turns it green.

Run the FOCUSED test inline in the loop — its output is small. When you
want broader confirmation before finishing (the full suite, or a heavy
check), don't run it inline: dispatch a subagent (the `Agent` tool, e.g.
a `general-purpose` agent — a fast model is plenty for running tests) to
run it and report back only a short digest — pass/fail plus any failing
test names and the one relevant error line. The verbose output stays in
the subagent's context, not yours.

After you finish, the pipeline runs the authoritative dependency install,
typecheck, lint, and full test suite for you — outside this session —
followed by an adversarial multi-lens review of your diff. Anything that
fails comes back to you here as a focused follow-up; you keep this
context.

Stay inside the slice. Spot an unrelated problem? Note it in your
response; do not fix it here. Run the repo's formatter before you finish.

Rules you will be tempted to break — don't:

| Rationalization | Reality |
|---|---|
| "I'll add the tests after it works" | A test written after the code passes immediately and only pins what the code already does. You never watched it fail, so it proves nothing. |
| "This slice is too simple to test" | Simple code breaks too; the test is the regression guard for the next change. |
| "I'll just run the whole suite inline to be sure" | That floods this session with output you don't need. Dispatch a subagent to run it and hand you back a one-line digest instead. |

Red flags — if any is true, stop and correct course: a test passed on its
first run · you can't say why the test failed · you're about to run the
full suite inline instead of in a subagent · you've written a lot of code
with no failing test driving it.

Report your outcome by writing `{stage_dir}/status.json`: `success`
when every slice is committed and its focused test is green:

```json
{ "outcome": "success" }
```

otherwise `fail` with a `failure_reason` describing what blocked you:

```json
{ "outcome": "fail", "failure_reason": "cannot satisfy the acceptance criteria without a schema migration the brief rules out of scope" }
```
