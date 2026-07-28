Implement the change issue $context.identifier — "$context.title" — asks for, in $context.repo.

The issue is at $context.url. The repo is checked out in your working directory.

1. Read the issue to understand the intended change and its acceptance
   criteria. Read the relevant existing code before touching anything —
   prefer extending existing patterns over inventing new ones.
2. Make the change test-first where practical: add or adjust tests that
   pin the behaviour, then make them pass. Keep the change scoped to
   what the issue asks — nothing missing, nothing beyond.
3. Run the project's tests and formatter before you finish.

Report your outcome by writing `{stage_dir}/status.json`: `success`
when the change is complete and the tests pass, otherwise `fail` with a
`failure_reason` describing what blocked you.
