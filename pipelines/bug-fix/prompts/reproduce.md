Reproduce the bug from $context.identifier with an automated, failing test.

Write a test that fails **because of this bug** and would pass once it is
fixed — the tighter it pins the actual root cause (not just the symptom),
the better. Add it in the project's test suite, following existing test
conventions. Run it and confirm it fails for the expected reason.

Do NOT fix the bug in this step — only capture it as a red test.

Report your outcome via `{stage_dir}/status.json`: `success` once you
have a test that fails for the right reason, otherwise `fail` with a
`failure_reason` (e.g. you cannot reproduce it from the information given).
