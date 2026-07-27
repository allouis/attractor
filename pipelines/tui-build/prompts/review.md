Review the milestone implementation.

1. `jj log` + `jj diff -r <first-milestone-commit>::@-` to see the
   full change set from this run.
2. Check against `docs/tui-spec.md`: does it match the milestone's
   scope and the architecture principle (TUI/client are thin API
   clients; the client stays stdlib-only through T3; the TUI talks to
   a daemon, never embeds the engine)? Nothing missing, nothing beyond
   scope?
3. Check quality: tests at the right seams (httptest for client,
   message-driven for models), race-clean, no dead code, honest commit
   messages. For T4, confirm `flake.nix` has a real vendorHash and
   `nix build .#attractor` passes.

Small issues: fix them yourself in additional small commits.
Fundamental problems (wrong approach, milestone half-done, build
broken): write status.json in your stage directory with
{"status": "fail", "failure_reason": "<specific findings>"} so the
pipeline loops back to implement.

If everything holds, summarise what was reviewed and confirm success.
