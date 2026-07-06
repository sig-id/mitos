# Pipeline: worker

Goal: execute ONE kanban task to a merged PR. Non-linear: the worker may loop
between execute → test → audit-fix until the quality gate passes or it cannot
continue. Driven by the **agentic orchestrator** (`Mitos.Orchestrator.Policy.Agentic`),
which decides the next stage from the folded state — not a fixed order.

```yaml
budget: { usd: 3, wall_ms: 1800000 }
```

## Stages (declared order is a default; the orchestrator may revisit)

1. **task-worker (plan/doc)** — in an isolated git worktree for this task, read
   the task body + repo, write a short plan + design notes. Decide scope.

2. **task-worker (execute)** — implement the task against the plan.

3. **task-worker (test)** — run the repo's tests; capture pass/fail + failures.

4. **quality-gate (audit/review)** — review the diff for correctness, security,
   and conformance to the task's acceptance criteria. Emit a verdict:
   `ship | fix | blocked`.

5. **task-worker (audit-fix)** — if the gate returns `fix`, address the review
   notes and re-run tests; the orchestrator loops back to the gate.

6. **task-worker (PR)** — on `ship`, push the branch and open a PR referencing
   the issue (a sink crossing — see GAPS.md); on `blocked`, emit the blocker.

> The orchestrator realizes the non-linearity: from a `fix` verdict it `insert`s
> an audit-fix step and `advance`s back to the gate rather than proceeding. A
> stuck worker (repeated `fix` with no progress) `escalate`s; an exhausted budget
> `abort`s.
