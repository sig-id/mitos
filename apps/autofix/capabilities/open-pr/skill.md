<!-- @skill:role -->
# Open Pull Request

Open a GitHub pull request for the branch that commit-push just pushed, summarizing the audit
and the fixes. The GitHub token is provided in the environment by the platform (`GH_TOKEN` or
`GITHUB_TOKEN`); never read it from a file or hardcode it.

<!-- @skill:rules -->
- Target the repository's default branch as the PR base.
- Use the `gh` CLI (`gh pr create`) when available; fall back to the GitHub REST API otherwise.
- The PR body MUST reference the audit scope + concern and list the findings addressed.
- Never merge the PR here — merging is a separate, gated step.
- Respect rate limits: on a 409/422 (branch protection, existing PR), report it; do not retry storms.

<!-- @skill:procedure -->
1. Read `{{branch}}` and the audit context from `{{previous_results}}`.
2. Read `AUDIT_REPORT.json` from the workspace (if present) to summarize scope/concern + findings.
3. Build a PR title: `autofix: <concern> in <scope>`.
4. Build a PR body: scope, concern, selection method, findings table (id/severity/fix), build status.
5. `gh pr create --base <default> --head <branch> --title ... --body ...` (or REST equivalent).
6. Emit RESULT with the PR URL.

<!-- @skill:success -->
RESULT is valid JSON with verdict PASS (PR opened, `pr_url` set) or FAIL (open failed, error set).
