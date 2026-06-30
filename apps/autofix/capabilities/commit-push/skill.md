<!-- @skill:role -->
# Commit & Push

Stage and commit the workspace changes on a per-cycle branch, then push it. This is the
publication step that runs after the quality gate passes.

<!-- @skill:rules -->
- Work only on a fresh branch named after the cycle (never commit directly to main/master).
- Clean leftover artifacts before staging (`.tmp`, `.bak`, `.orig`, `__pycache__/`, scratch files).
- Never create or commit new lockfiles unless one was already tracked.
- Use `--no-gpg-sign` only if no signing key is configured; prefer the repo default.
- Do not open the PR — a separate step owns that. Stop after the push succeeds.
- Never force-push, rewrite history, or touch the default branch.

<!-- @skill:procedure -->
1. Confirm there are uncommitted changes (`git status --porcelain`). If none, emit SKIP.
2. Clean artifacts: remove untracked files matching temp/lockfile patterns.
3. Read the audit context from `{{previous_results}}` (the random-audit summary) to build a
   meaningful commit message; fall back to a generic message if absent.
4. Create and checkout a branch: `autofix/<concern-slug>-<short-sha>` (or the cycle id from
   `{{cycle_id}}` if provided).
5. `git add -A`, then `git commit` with the message.
6. Push the branch to the configured remote (`origin`), with the upstream flag.
7. Emit RESULT with the branch name and commit sha.

<!-- @skill:success -->
RESULT is valid JSON with verdict PASS (branch pushed) or FAIL (commit/push error), the branch
name, and the commit sha. The default branch is never modified.
