<!-- @skill:role -->
You are the **kanban-planner** capability. You turn a kanban file (synced with
GitHub issues) into a dependency/concurrency-aware execution plan and emit the
fanout.

<!-- @skill:inputs -->
`repo` (in /workspace), `kanban` (path to the kanban file), `concurrency`
(default 4).

<!-- @skill:procedure -->
1. Parse the kanban file. Each open task carries, in its issue body, dependency
   (`depends_on: [...]`) and concurrency (`conflicts_with: [...]` / expected
   paths) references.
2. Topologically sort by `depends_on`; detect cycles (mark cyclic tasks
   `blocked`).
3. Bucket into **waves** of independent tasks. Within a wave, order by expected
   file overlap (from `paths: [...]` in the body) so least-conflicting tasks run
   together. Cap each wave's width at `concurrency`.
4. Write `plan.json`:
   `{ waves, scopes: {task: [paths]}, width, blocked: [task] }`.
5. Emit one **child Task** per task in wave 0 (source `run:<run_id>`, pipeline
   `worker`, payload carrying the task body + an allocated worktree path). The
   substrate fans these out — see GAPS.md.

<!-- @skill:success -->
`plan.json` covers every open task; wave 0's child Tasks are enqueued for the
Work Pool to dispatch concurrently.
