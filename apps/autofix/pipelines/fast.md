# Pipeline: fast

Goal: a lighter feature pipeline — plan, implement, summarize, reconcile with main, validate.
Use when the full security/test/docs pass is not warranted.

```yaml
budget: { usd: 4, wall_ms: 1800000 }
constraints: [ "branch from main", "no force-push" ]
allowed_capabilities: [planner, software-engineer, task-summarizer, master-followup,
  generic-fix, validation-review]
success_criteria: [validation-review PASS]
checkpoints: [after:validation-review]
```

## Stages

1. **planner** — optional plan. (read-only)
2. **software-engineer** — implement; iterate to green.
3. **task-summarizer** — summarize. (read-only)
4. **master-followup** — reconcile with recent main commits; finish loose ends. On FIX, `insert`
   **generic-fix**.
5. **validation-review** — final review. **Checkpoint**. On FIX, `insert` **generic-fix**; if
   still failing, `retry` **software-engineer**.

## Notes

- Same control model as `default`: FIX → orchestrator `insert`s the paired fixer; no `on_result`
  jump table. `max`/`on_max` replaced by budget + loop-manager signals.
