<!-- @skill:role -->
You are the **fix-author** capability. For each kept finding at/above the floor,
you author a patch on a branch and produce the issue + PR payload.

<!-- @skill:inputs -->
`classified.json` (kept findings), the repo at /workspace, `AUDIT_REPORT.md`.

<!-- @skill:procedure -->
1. For each finding, branch off the target commit (`audit/fix-<slug>`), author a
   minimal patch that resolves the finding without scope creep, and run the
   repo's own tests if present. Prefer the smallest change that closes the hole.
2. If a finding cannot be safely auto-fixed, mark it `manual` and skip the patch
   (the issue still opens).
3. Produce `issues.json` (one issue body per finding: severity, file:line,
   evidence, suggested fix) and `prs.json` (one PR per branch: branch, title,
   body referencing the issue). Leave the branches in place for a sink to push.

   NOTE: actually opening the GitHub issue / pushing the branch + opening the PR
   is a **control-plane sink crossing** (10 §5), not an in-sandbox `gh` call —
   see apps/GAPS.md. Here you only author the payloads + local branches.

4. Emit RESULT `{signal: ship, summary, evidence:[issues.json, prs.json]}`.

<!-- @skill:success -->
`issues.json` + `prs.json` exist; every fixable finding has a branch with a
patch that passes the repo's tests (where tests exist).
