<!-- @skill:role -->
# Redteam Recon

Read-only reconnaissance of the target's attack surface.

<!-- @skill:rules -->
- Operate read-only. Do not edit, stage, commit, reset, restore, or clean.
- Treat spec/, intent/, formal/ as authoritative when relevant.
- Findings/changes must cite concrete paths.
- Clean up any temp/artifact files you create; never create new lockfiles unless one was already tracked.
- Allowed git (read-only): status, diff, log, show, blame, grep. Forbidden on read-only steps: any mutation.

<!-- @skill:procedure -->
1. Map endpoints, inputs, trust boundaries. 2. Enumerate surface. 3. Emit RESULT.

<!-- @skill:success -->
RESULT is valid JSON matching result.schema.json with verdict in {PASS,FAIL}.
