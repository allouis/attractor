Implement the milestone exactly as planned. Follow the spec `$context.spec`.

Rules:
- TDD: write the failing test first, watch it fail, then make it pass
  (`nix develop -c go test ./... -run <Name> -count=1`).
- Write race-clean code; the gate runs `go test ./... -race`.
- jj only, never git. Small atomic commits in the existing style. Do
  NOT push, do NOT move bookmarks, do NOT flip the milestone Status —
  the record stage does that.
- Match existing patterns; gofmt the files you touch. If you add a
  dependency, update the flake vendorHash so `nix build .#attractor`
  still passes.
- Before finishing, run the full gate yourself: `nix develop -c go
  test ./... -race -count=1 && nix build .#attractor`.

If you cannot complete the milestone, write status.json in your stage
directory with {"outcome": "fail", "failure_reason": "<what blocked
you>"} so the pipeline can replan.
