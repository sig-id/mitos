# Pipeline: default

Goal: implement a task/issue end-to-end with a full engineering pipeline — plan, implement,
summarize, security-audit (+fix), test-coverage (+fix), document, and a final independent
validation — then the change is ready for PR.

```yaml
budget: { usd: 8, wall_ms: 3600000 }
constraints:
  - branch from main; never commit to main
  - no force-push, no history rewrite
allowed_capabilities: [planner, software-engineer, task-summarizer, security-audit,
  security-fix, test-coverage, generic-fix, documentation-writer, validation-review]
success_criteria:
  - validation-review reports PASS (change meets the objective at production bar)
checkpoints: [after:validation-review]
```

## Stages

1. **planner** — read the task objective + repo, write a short implementation plan. (read-only;
   gated by `WIGGUM_PLAN_MODE` in the original — here it is an optional stage the orchestrator
   skips if the task is small.)
2. **software-engineer** — implement the plan; iterate until local build+tests pass.
3. **task-summarizer** — produce a concise summary of the completed work. (read-only)
4. **security-audit** — read-only security review of the diff. On FIX, the orchestrator `insert`s
   **security-fix** to remediate, then re-audits.
5. **test-coverage** — add/strengthen tests for the changed code. On FIX, `insert` **generic-fix**
   to close gaps.
6. **documentation-writer** — update docs touched by the change.
7. **validation-review** — independent final review. **Checkpoint**. On FIX, `insert`
   **generic-fix**; if still failing, `retry` **software-engineer** with feedback.

## Notes for the orchestrator

- The original `on_result: { FIX: { inline agent } }` handlers become orchestrator `insert`
  Decisions: when a review step returns FIX, open the paired fix capability as a remediation
  step rather than a pre-wired jump.
- Bounding: `max`/`on_max` are gone. Use the loop-manager escalation ladder (effort → model →
  harness → escalate) and budget; if a step keeps returning FIX, `escalate` and re-scope.
