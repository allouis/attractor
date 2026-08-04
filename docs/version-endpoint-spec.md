# Version endpoint

Expose the daemon's build revision over HTTP so an operator — or the
build-skew guardrail — can read the running server's revision without shell
access. The revision is already stamped into the binary
(`internal/version.Revision`, set via ldflags in `flake.nix`); this just
surfaces it on the HTTP API next to `/healthz`.

## Motivation

`/healthz` proves the daemon is up but says nothing about *which build* is
running. When a VM image bakes a stale attractor the daemon already warns on
phone-home skew; a plain `GET /version` lets any client (the web UI, a
monitoring probe, a human with curl) read the running revision directly.

## Milestones

| # | Deliverable | Status |
|---|---|---|
| V1 | `GET /version` returns `{"version": "<ver>", "revision": "<rev>"}` sourced from `internal/version`. Unauthenticated like `/healthz` (bypasses the bearer gate). Add httptest coverage asserting the JSON shape and that the revision field is populated. | done |

## Notes

- Mirror the `/healthz` handler registration and its auth-bypass so the
  endpoint is reachable without a token.
- Keep the payload minimal and stable; other fields can be added later.
