# Pipeline: fix

Goal: take an open PR through its resolution lifecycle — address reviewer comments, sync with
main, resolve conflicts, run tests, commit+push, merge.

```yaml
budget: { usd: 4, wall_ms: 1800000 }
constraints: [ "no force-push to main", "merge only after CI green" ]
allowed_capabilities: [pr-comment-fix, git-sync-main, git-conflict-resolver,
  test-runner, generic-fix, commit-push, pr-merge]
success_criteria: [ "PR merged, or correctly abandoned as unresolvable" ]
checkpoints: [after:test-runner]
```

## Stages

1. **pr-comment-fix** — address actionable reviewer comments. SKIP if none. On FIX, `retry` self.
2. **git-sync-main** — fetch + merge/rebase default branch. On FAIL (conflicts), `insert`
   **git-conflict-resolver**; if that FAILs, `abort`.
3. **test-runner** — run the suite. On FIX, `insert` **generic-fix**; on FAIL, `abort`.
4. **commit-push** — commit and push the resolved branch. On FAIL, `retry` **git-sync-main**.
5. **pr-merge** — merge once CI is green. On FAIL, `retry` **git-sync-main**.

## Notes

- The original `on_result` jump targets (`jump: sync-main`, `jump: abort`, `jump: self`) become
  orchestrator Decisions: `insert` (open a paired capability), `retry` (re-run with feedback),
  `abort` (terminal). No jump-FSM.
