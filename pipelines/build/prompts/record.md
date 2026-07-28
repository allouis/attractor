Close out the milestone.

1. In the spec `$spec`, flip the completed milestone's Status from
   `todo` to `done` in the milestone table. Touch nothing else.
2. Commit exactly that change with jj, message:
   `docs: mark <milestone id> done` with a one-line body naming what
   shipped.
3. **Verify the commit is clean, not a phantom.** Run `jj show @-` and
   confirm the `mark done` commit actually contains the Status flip,
   and `jj status` shows the working copy with **no changes**. If
   instead the flip shows as an *uncommitted* working-copy change (a
   known jj working-copy snapshot quirk), fold it in and re-verify:
   `jj squash --into @-` then `jj status` (must be clean). Do not leave
   the ledger flip uncommitted.
4. Do not push. Do not start the next milestone.

Reply with the milestone id and the final commit's change id.
