Implement the milestone exactly as planned in the previous stage.

Rules:
- TDD: write the failing test first, watch it fail, then make it
  pass. Use `nix develop -c go test ./tests/ -run <Name> -count=1`.
- Small atomic commits with jj (`jj commit -m "..."`), never git.
  Follow the existing commit message style (conventional commits,
  imperative, body explains why).
- Do NOT push, do NOT move bookmarks, do NOT edit the milestone
  Status column — a later stage does that.
- Follow existing code patterns; run gofmt on files you touch.
- Before finishing, run the full suite: `nix develop -c go test ./...
  -count=1`. If it is red, keep working or report failure.

If you hit a design ambiguity the spec does not answer, make the
smallest reasonable choice and record it in the commit message.

If you cannot complete the milestone, write status.json in your stage
directory with {"status": "fail", "failure_reason": "<what blocked
you>"} so the pipeline can replan.
