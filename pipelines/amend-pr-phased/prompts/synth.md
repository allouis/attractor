Three reviewers each reviewed the same amendments through a different lens:
whether they do what the feedback asked, whether they are correct, and whether
they are tested. Their findings are in $context.parallel.results (JSON — each
branch's context_updates holds one review.<lens> entry).

Before including a finding, verify it against the actual code. The reviewers saw
a diff; you have the repository. Discard whatever is speculative or does not
hold up.

## Intent was already settled

The approved plan for these amendments is in your context, and a human approved
it at a gate. It is authoritative about INTENT. Reviewers see only a diff, so
they cannot tell a deliberate decision from an accident, and they will sometimes
object to something chosen on purpose.

Telling those apart is your job: **reviewers audit execution, the plan settles
intent.** A finding that objects to a decision the plan states is a NOTE — put
it in the summary, name the decision it contradicts, and do not block. A finding
that the amendments fail to do what the plan decided, or are wrong on their own
terms, still blocks.

Changing approved behaviour because a reviewer objected is worse than leaving
the objection open: it silently undoes a decision a human made, and nobody
notices.

## What blocks

A finding blocks only if it is a defect in the amendments themselves, evidenced
at file:line, that would produce wrong behaviour or a failure in production —
**or** if a feedback point was missed or only partly addressed, since answering
the feedback is the entire purpose of this work.

A blocker is something the amendments got WRONG, never something they DECIDED.

Everything else is a note: a problem that already existed in the PR before these
amendments, a request for scope beyond the feedback, a preference about how
working code is written, anything about how the work is arranged (that is the
history phase, later).

If you are unsure whether something blocks, it does not block. A note that turns
out to matter costs a review comment; a false block costs a rework round, and
rework rounds degrade the change.

## Report

Merge what survives into one review, deduping overlapping points. Write
`{stage_dir}/status.json` (that exact path).

Nothing blocks:

```json
{ "outcome": "success",
  "context_updates": { "review.verdict": "pass",
                       "review.summary": "### Notes\n\n- …" } }
```

Something blocks:

```json
{ "outcome": "fail",
  "failure_reason": "one line",
  "context_updates": { "review.verdict": "fail",
                       "review.blocking": "### Blocking\n\n1. …",
                       "review.summary": "### Notes\n\n- …" } }
```

review.blocking is the ONLY channel through which the responder sees your
findings — abbreviating it loses them.
