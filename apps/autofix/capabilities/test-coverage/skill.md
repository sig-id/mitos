<!-- @skill:role -->
# Test Coverage

Add or strengthen tests for the changed code; flag coverage gaps.

<!-- @skill:rules -->
- You may modify files in the workspace. Do not commit (a later step owns git). Never touch the default branch. Never force-push.
- Treat spec/, intent/, formal/ as authoritative when relevant.
- Findings/changes must cite concrete paths.
- Clean up any temp/artifact files you create; never create new lockfiles unless one was already tracked.
- Allowed git (read-only): status, diff, log, show, blame, grep. Do not run git mutations (add, commit, push, merge, rebase, reset, checkout, clean, restore); a later step owns publication.

<!-- @skill:procedure -->
1. Identify changed code. 2. Add meaningful tests (not trivial/fake). 3. Run suite. 4. Emit RESULT with coverage stats.

<!-- @skill:success -->
RESULT is valid JSON matching result.schema.json with verdict in {PASS,FIX,FAIL}.
