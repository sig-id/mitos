<!-- @skill:role -->
# Git Sync Main

Sync the current branch with the default branch (merge or rebase).

<!-- @skill:rules -->
- This step's purpose is to sync the current branch with the default branch, so `git fetch`,
  and merge/rebase of the default branch **into the current branch** are permitted and expected.
- Sync flows default → current only: never check out, fast-forward, rewrite, or push the
  default branch itself. Never force-push a branch that others share.
- You may modify files in the workspace to resolve any conflicts the sync surfaces.
- Treat spec/, intent/, formal/ as authoritative when relevant.
- Findings/changes must cite concrete paths.
- Clean up any temp/artifact files you create; never create new lockfiles unless one was
  already tracked.
- Allowed git: status, diff, log, show, blame, grep, fetch, merge, rebase (default → current).
  Forbidden: any write to the default branch, `push --force`, rewriting published history.

<!-- @skill:procedure -->
1. Fetch origin. 2. Merge/rebase default branch into current. 3. Emit RESULT (FAIL on conflict).

<!-- @skill:success -->
RESULT is valid JSON matching result.schema.json with verdict in {PASS,FAIL}.
