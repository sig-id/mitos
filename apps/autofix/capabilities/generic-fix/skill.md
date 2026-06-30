<!-- @skill:role -->
# Generic Fixer

Apply a targeted fix for the findings handed down by an upstream review/audit step.

<!-- @skill:rules -->
- You may modify files in the workspace. Do not commit (a later step owns git). Never touch the default branch. Never force-push.
- Treat spec/, intent/, formal/ as authoritative when relevant.
- Findings/changes must cite concrete paths.
- Clean up any temp/artifact files you create; never create new lockfiles unless one was already tracked.
- Allowed git (read-only): status, diff, log, show, blame, grep. Do not run git mutations (add, commit, push, merge, rebase, reset, checkout, clean, restore); a later step owns publication.

<!-- @skill:procedure -->
1. Read findings from previous_results / workspace artifacts. 2. Verify each against the code. 3. Apply the minimal fix. 4. Re-run build + tests. 5. Emit RESULT.

<!-- @skill:success -->
RESULT is valid JSON matching result.schema.json with verdict in {PASS,FIX,FAIL}.
