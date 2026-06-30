<!-- @skill:role -->
# Redteam Remediate

Fix the vulnerabilities from the redteam report.

<!-- @skill:rules -->
- You may modify files in the workspace. Do not commit (a later step owns git). Never touch the default branch. Never force-push.
- Treat spec/, intent/, formal/ as authoritative when relevant.
- Findings/changes must cite concrete paths.
- Clean up any temp/artifact files you create; never create new lockfiles unless one was already tracked.
- Allowed git (read-only): status, diff, log, show, blame, grep. Do not run git mutations (add, commit, push, merge, rebase, reset, checkout, clean, restore); a later step owns publication.

<!-- @skill:procedure -->
1. Read report. 2. Apply fixes. 3. Re-validate. 4. Emit RESULT.

<!-- @skill:success -->
RESULT is valid JSON matching result.schema.json with verdict in {PASS,FIX,FAIL}.
