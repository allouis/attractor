You are working in the attractor repo. Plan the next milestone. Do
not write any code or make any commits in this stage.

1. Read `docs/service-spec.md` in full. Find the Milestones table and
   pick the first row whose Status is `todo` and whose dependencies
   are all `done`.
2. Read `jj log --limit 30` and skim the recent commits so you know
   what already exists. Read the source files the milestone touches.
3. Produce a concrete implementation plan for THIS MILESTONE ONLY:
   the commits you will make (small, atomic, refactor-first where it
   helps), the tests you will write first (TDD, red before green),
   and the files involved.

State clearly which milestone you selected. If no milestone is
eligible (all done, or dependencies unmet), write a status.json in
your stage directory with {"status": "fail", "failure_reason":
"no eligible milestone"} and stop.
