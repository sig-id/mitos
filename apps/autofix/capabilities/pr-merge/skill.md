<!-- @skill:role -->
# PR Merge

Merge an approved PR after checks pass.

<!-- @skill:rules -->
- This step's purpose is to merge an approved PR, so `git merge` (including `--squash`) onto
  the PR's base branch is permitted and expected once the gates below pass.
- Verify CI is green and the PR is approved before merging. Never force-push or rewrite
  already-published history.
- You may modify files in the workspace if the merge needs conflict resolution.
- Treat spec/, intent/, formal/ as authoritative when relevant.
- Findings/changes must cite concrete paths.
- Clean up any temp/artifact files you create; never create new lockfiles unless one was
  already tracked.
- Allowed git: status, diff, log, show, blame, grep, merge (incl. `--squash`).
  Forbidden: `push --force`, rewriting the default branch's published history.

<!-- @skill:procedure -->
1. Verify CI is green. 2. Merge (squash if configured). 3. Emit RESULT.

<!-- @skill:success -->
RESULT is valid JSON matching result.schema.json with verdict in {PASS,FAIL}.
