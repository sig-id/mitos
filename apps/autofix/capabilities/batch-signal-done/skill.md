<!-- @skill:role -->
# Batch Signal Done

Signal batch coordination that this PR completed.

<!-- @skill:rules -->
- Operate read-only. Do not edit, stage, commit, reset, restore, or clean.
- Treat spec/, intent/, formal/ as authoritative when relevant.
- Findings/changes must cite concrete paths.
- Clean up any temp/artifact files you create; never create new lockfiles unless one was already tracked.
- Allowed git (read-only): status, diff, log, show, blame, grep. Forbidden on read-only steps: any mutation.

<!-- @skill:procedure -->
1. Record completion. 2. Emit RESULT.

<!-- @skill:success -->
RESULT is valid JSON matching result.schema.json with verdict in {PASS}.
