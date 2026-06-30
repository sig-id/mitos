<!-- @skill:role -->
# Security Audit

Read-only security review of the diff/change for vulnerabilities and threat-model gaps.

<!-- @skill:rules -->
- Operate read-only. Do not edit, stage, commit, reset, restore, or clean.
- Treat spec/, intent/, formal/ as authoritative when relevant.
- Findings/changes must cite concrete paths.
- Clean up any temp/artifact files you create; never create new lockfiles unless one was already tracked.
- Allowed git (read-only): status, diff, log, show, blame, grep. Forbidden on read-only steps: any mutation.

<!-- @skill:procedure -->
1. Run the diff. 2. Check injection, authz, secrets, crypto, deserialization, SSRF, etc. 3. Classify findings by severity. 4. Emit RESULT.

<!-- @skill:success -->
RESULT is valid JSON matching result.schema.json with verdict in {PASS,FIX,FAIL}.
