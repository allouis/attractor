Implement the milestone exactly as planned. Follow `docs/tui-spec.md`.

Rules:
- TDD: write the failing test first, watch it fail, then make it pass.
  `nix develop -c go test ./... -run <Name> -count=1`.
- Several targets are concurrent (SSE streaming, the event channel):
  write race-clean code and check with
  `nix develop -c go test ./... -race -count=1`.
- Bubble Tea models are pure `Update(msg) (model, cmd)`; test by
  feeding messages and asserting model state / `View()` substrings.
  Never require a real terminal in a test.
- The API client is tested against `httptest.Server`. Keep
  `internal/client` dependency-free (stdlib only) so T1–T3 land before
  any external dep.
- For T4 (first external deps): after `go get` + `go mod tidy`, update
  `flake.nix` so `nix build .#attractor` and the `attractor-test`
  check pass — set `vendorHash` to a fake, `nix build`, read the real
  hash from the error, paste it in (factor it into one shared binding
  if both sites need it). Confirm `nix flake check` is green.
- jj only, never git. Small atomic commits in the existing style. Do
  NOT push, do NOT move bookmarks, do NOT edit the milestone Status
  column — the record stage does that.
- Before finishing, run the full gate yourself:
  `nix develop -c go test ./... -race -count=1 && nix build .#attractor`
  and gofmt the files you touched.

If you cannot complete the milestone, write status.json in your stage
directory with {"status": "fail", "failure_reason": "<what blocked
you>"} so the pipeline can replan.
