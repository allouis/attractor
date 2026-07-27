Review PR #$pr_number — "$title" — in $repo ($url).

The PR branch is already checked out in your working directory (the
previous stage ran `gh pr checkout`).

1. `gh pr diff $pr_number --repo $repo` to see the change set. Read the
   touched files for surrounding context, not just the diff hunks.
2. Assess correctness (edge cases, error paths, races), scope (does the
   change match its stated intent — nothing missing, nothing beyond),
   and quality (tests at the right seams, no dead code, comments only
   where behaviour is surprising).
3. Summarise your findings: what the PR does, what holds up, and any
   concrete issues an author should address (file:line where you can).

This is a read-only review — do not push commits or change the PR. Your
summary is the deliverable.
