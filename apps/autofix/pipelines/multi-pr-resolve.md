# Pipeline: multi-pr-resolve

Goal: batch-coordinated PR resolution — sync, resolve conflicts, test, commit/push, merge —
with explicit batch signaling so the coordinator knows each PR's outcome.

```yaml
budget: { usd: 3, wall_ms: 1200000 }
constraints: [ "honor batch coordination signals", "merge only when coordinator allows" ]
allowed_capabilities: [git-sync-main, git-conflict-resolver, test-runner, generic-fix,
  commit-push, pr-merge, batch-signal-done, batch-signal-failed]
success_criteria: [ "PR merged or correctly signaled failed to the batch coordinator" ]
```

## Stages

1. **git-sync-main** — sync with default branch. On PASS, go to commit-push.
2. **git-conflict-resolver** — resolve conflicts (inserted on sync FAIL). On FAIL, `insert`
   **batch-signal-failed** then `abort`.
3. **test-runner** — run suite. On FIX, `insert` **generic-fix**; on FAIL, `insert`
   **batch-signal-failed** then `abort`.
4. **commit-push** — push. On FAIL, signal failed + abort.
5. **pr-merge** — merge. On FAIL, signal failed + abort.
6. **batch-signal-done** — tell the coordinator this PR completed.

## Notes

- The original inline `batch-signal-failed` handlers (`on_result: { PASS: jump:abort }`) become
  orchestrator `insert` Decisions followed by `abort`. The dependency graph (`spec/06 §4`) is
  the proper home for batch coordination: each PR is a Task with `conflict` mutexes on shared
  files and ordering edges to the coordinator.
