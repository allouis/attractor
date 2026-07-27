You are working in the attractor repo. Plan the next TUI milestone.
Write no code and make no commits in this stage.

1. Read `docs/tui-spec.md` in full — the architecture principle, the
   build conventions, the dependency/build note, and the milestone
   table. Also skim `docs/service-spec.md` for context on the server
   and API the TUI builds on.
2. Find the milestone table in `docs/tui-spec.md` and pick the first
   row whose Status is `todo` and whose dependencies are all `done`.
3. Read `jj log --limit 20` and the source the milestone touches
   (`internal/server/`, `internal/client/`, `internal/tui/`,
   `internal/cli/`, `flake.nix` for T4) so you know what exists.
4. Produce a concrete TDD plan for THIS MILESTONE ONLY: the failing
   tests you will write first (per the spec's testing conventions —
   httptest for the client, message-driven for Bubble Tea models,
   never a real terminal), the small atomic commits, and the files.

State clearly which milestone you selected. If T4 is selected, note
the vendorHash step explicitly (add deps → set vendorHash to a fake →
nix build → read the real hash → paste it → nix flake check green).

If no milestone is eligible (all done, or dependencies unmet), write
status.json in your stage directory with {"status": "fail",
"failure_reason": "no eligible milestone"} and stop.
