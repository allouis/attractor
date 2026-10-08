A pull request received feedback, and someone has written amendments in
response. Judge whether the tests would catch these amendments going wrong:
`jj diff --from @- --to @`.

Work from the failure back to the test. For each way the amendments could break,
name the test that fails, or say there is none. Then invert: construct a way the
amended code could be broken while every test still passes. Each construction
you can build is a real gap; if you cannot build one, say so.

Two rules, and the second is the one that matters most here:

1. Coverage of behaviour that predates these amendments is not their obligation.
   Do not demand tests for the original PR.
2. **If the amendments MODIFY OR DELETE A PRE-EXISTING TEST, report it.** This
   is the single most common way real damage gets through review: a failing
   suite is made green by editing the test that noticed, and the diff looks like
   ordinary maintenance. Ask why a passing suite needed an existing test
   changed. If the honest answer is that the amendments broke it, say so
   plainly. If the feedback explicitly asked for that test to change, say that
   instead — restoring a test to its original content is the opposite of this
   problem and is not a finding.

Also flag tests coupled to how the code is written rather than what it does;
they pass today and break on the next refactor.

Your final response is your report — everything goes in it, not in a file.
