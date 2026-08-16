You are working in the attractor repo. Plan the next milestone from the
spec `$context.spec`. Write no code and make no commits in this stage.

1. Read `$context.spec` in full — the design and the milestone table (the one
   with a Status column).
2. Pick the first milestone whose Status is `todo` and whose
   dependencies are all `done`. Read `jj log --limit 20` and the source
   the milestone touches so you know what already exists.
3. Produce a concrete TDD plan for THIS MILESTONE ONLY: the failing
   tests you will write first, the small atomic commits, and the files.

State clearly which milestone you selected. If no milestone is eligible
(all done, or dependencies unmet), write status.json in your stage
directory with {"outcome": "fail", "failure_reason": "no eligible
milestone"} and stop.
