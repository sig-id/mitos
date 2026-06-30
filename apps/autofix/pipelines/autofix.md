# Pipeline: autofix

Goal: one cycle of autonomous code improvement — randomly audit a scope+concern, verify and
fix real findings, gate the diff, and publish a PR. The **loop** that re-runs this pipeline is
owned by the `autofix-loop` service unit (`services/autofix-loop.json`), not by this pipeline.
A pipeline is one cycle; the service is the daemon.

```yaml
budget: { usd: 5, wall_ms: 1800000 }
constraints:
  - read-only audit; no mutating git during audit/verify
  - never commit to, force-push, or rewrite the default branch
  - each cycle on its own branch named autofix/<concern-slug>
  - publication (commit/push/PR) only after the quality gate passes
allowed_capabilities: [random-audit, verify-fix, quality-gate, commit-push, open-pr, generic-fix]
success_criteria:
  - a PR exists targeting the default branch, OR the cycle correctly produced no actionable findings
checkpoints: [after:quality-gate]
```

## Stages

1. **random-audit** — RNG-select a scope (weighted `shuf`) and a concern, audit it read-only,
   write findings to `AUDIT_REPORT.json` + the RESULT artifact. This is the entry point every
   cycle. (read-only)
2. **verify-fix** — read `AUDIT_REPORT.json` from the shared workspace, independently verify
   each finding, red-green-TDD fixes for the real ones. Verdict SKIP if all findings were false
   positives.
3. **quality-gate** — blind, context-free review of the resulting `git diff`. PASS only if the
   diff measurably improves the codebase and build/tests are green. **Checkpoint**: a smart
   verdict runs here; on FAIL the orchestrator discards the cycle (the service starts a fresh
   one) — there is no jump-back, the service simply re-fires.
4. **commit-push** — clean artifacts, commit on a fresh `autofix/<slug>` branch, push to origin.
   SKIP if there is nothing to commit (audit found nothing worth fixing).
5. **open-pr** — open a GitHub PR summarizing the audit scope/concern and findings addressed.

## Notes for the orchestrator

- If `random-audit` returns PASS (no findings), skip straight to `finish` — there is nothing
  to publish this cycle. The service unit will re-fire.
- If `quality-gate` returns FAIL, `abort` the run (discard); do not retry within the same
  cycle. The loop-manager's `verify_fail`/`oscillation` signals bound retries inside a step.
- Data handoff: the audit report travels as the **`AUDIT_REPORT.json` workspace artifact**
  (structured) and as the **`{{previous_results}}`** summary (verdict + one-liner). No rigid
  tags; no `on_result` jump table.
- The loop, concurrency, and per-cycle worktree isolation are properties of the service unit
  and the work pool (`spec/06`), not of this pipeline.
