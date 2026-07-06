# Pipeline: plan

Goal: read the kanban file, build a dependency/concurrency-aware execution plan,
and fan out N (default 4) workers over independent tasks — choosing an order that
minimizes merge conflicts (rough scope analysis per task).

```yaml
budget: { usd: 20, wall_ms: 7200000 }
```

## Stages

1. **kanban-planner** — parse the kanban file (synced with GitHub issues; each
   task body carries dependency/concurrency references), topologically sort by
   deps, and bucket tasks into waves that can run concurrently. Within a wave,
   order tasks to minimize file-overlap (rough: group by the paths each task is
   expected to touch). Emit `plan.json`:
   `{ waves: [ [task_id, ...], ... ], scopes: { task_id: [paths] }, width: N }`.

   THEN, for the substrate to fan out: this stage emits one **child Task** per
   task in wave 0 (source `run:<run_id>`), each pinned to the `worker` pipeline.
   The Work Pool dispatches up to `width` concurrently (06 §5). **This fanout is
   the load-bearing gap** — the MVP Work Pool is a single intake with no
   disciplines/invoke, so today the plan runs serially. See apps/GAPS.md.

> The worker pipeline (`worker.md`) is what each fanned-out Task runs. It is
> deliberately non-linear (plan ⇄ execute ⇄ test ⇄ audit, with fix loops), driven
> by the agentic orchestrator policy.
