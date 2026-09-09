# Durable tasks and local work pools

Every local submission now creates or requeues a durable Task. A Task owns the
brief, local tenant, pool, priority, dependency edges, absolute deadline, and
attempt lineage. Its status is independent of the execution record: a task can
be `blocked` while its run receipt remains `queued`, or `scheduled` while its
execution is preparing. Retries keep the Task ID and record another admitted run;
checkpoint recovery stays within the same run.

The local UI/API still allocates a run receipt at enqueue so existing run URLs
can show waiting and cancellation. No workspace preparation or command starts
at enqueue. `current_run_id` identifies that receipt; `runs` contains only admitted
attempts, so newly queued tasks have zero runs. General intake with deferred run
record allocation and non-workspace payloads remains part of the full work-pool
migration.

## Shared intake and readers

```sh
./bin/mitos tasks
./bin/mitos task task_TASK_ID
./bin/mitos pools
./bin/mitos configure-pool --discipline lifo --concurrency 1 batch
./bin/mitos run --title 'Follow-up work' --app local-check --workspace . \
  --pool batch --priority 10 --after task_UPSTREAM_ID
```

`GET /api/tasks`, `GET /api/tasks/{id}`, and `GET /api/pools` use the same store
projections. `POST /api/tasks` accepts the existing JSON brief and returns the
Task; `POST /api/runs` accepts the same input and returns the queued run receipt.
The Work page links to `/tasks`, which shows queue state, prerequisites, attempt
lineage, and editable pool policies. New-work forms expose the same scheduling
fields. `PUT /api/pools/{id}` persists a strict pool object whose ID matches the
path. Unknown fields and unsupported policies are rejected.

A JSON brief may include:

```json
"scheduling": {
  "pool": "batch",
  "priority": 10,
  "deadline": "2026-09-10T18:00:00+02:00",
  "deps": [
    {"from": "task_UPSTREAM_ID", "relation": "FS", "payload": "control",
     "strength": "hard", "guard": "done"}
  ],
  "join": {"rule": "all"}
}
```

## Readiness and dependency evidence

Dependencies are immutable task-bound edges and may cross pools within the local
tenant. Intake rejects unknown tasks, cycles, duplicate sources, cross-tenant
references, and use of illustrative sample work as a real prerequisite. The
current adapter admits finish-to-start (`FS`) control and explicit artifact data edges. Unsupported relation,
payload, guard, or join shapes fail before a task receipt is written.

An omitted guard means any terminal task result (`done`, `failed`, or `cancelled`).
A status guard selects that result only. CLI `--after TASK_ID` and the new-work
form use `guard: done`; `--after-failure TASK_ID` uses `failed`. A false hard guard
keeps the task blocked. An upstream retry can later satisfy it; the drainer does
not silently skip the dependent or reinterpret failure as success.

Hard dependencies gate readiness. Soft dependencies never block and contribute
a bounded preference to the priority discipline. Joins are `all`, `any`, and
`k_of_n` with a positive `k` no larger than the hard-dependency count. CLI flags
are `--join any` or `--join k_of_n --quorum K`. Race/quorum winners are the earliest
matching terminal task-event sequences available at admission, with stable ID
tie-breaking. Unmatched branches are not automatically cancelled.

Before acquiring ownership, dispatch rechecks readiness and pool capacity. It
saves each selected prerequisite's task ID, producing run ID, terminal status,
and exact task-event sequence/hash. Those witnesses are supplied in the worker's
stdin envelope as `dependencies`, alongside `task_id`. Control edges inject no artifact bodies. Explicit data edges additionally bind
registered artifacts and provide verified private input copies; see
[DATA_DEPENDENCIES.md](DATA_DEPENDENCIES.md). Once a run is admitted, its
witnesses remain fixed across checkpoint recovery. An upstream retry cannot
silently substitute a different result. A new downstream attempt rechecks current
upstream state and records a new binding.

## Selection and capacity

The local provider has two managed execution slots. Installed defaults are
`interactive` (priority), `batch` (FIFO), and `calls` (LIFO), each with concurrency
two and no reserved slots. Naming the calls pool does not implement invoke.

Supported disciplines:

- `fifo`: oldest ready task first.
- `lifo`: newest ready task first.
- `priority`: configured Task priority plus any saved service adjustment, one
  aging point per ready minute, and one point per satisfied soft dependency. Base
  Task priority is bounded to -1000..1000; the combined score is not clamped.
- `deadline_edf`: earliest absolute deadline; undated work follows dated work.

Service Tasks can carry a bound named queue preference. See
[service queue priority](SERVICE_PRIORITY.md) for its fixed adjustments and
intake/retry/replay behavior. Other pool disciplines retain their own ordering.

All ties are deterministic. Pool turns use the last recorded admission sequence
so a busy pool cannot reset its turn by restarting Studio. Ready-set snapshots
are selected by pool turn, then by the pool's discipline. Admission rejects a
selection if its pool policy changed after selection, and records the exact policy
used. New arrivals participate in the next ready-set snapshot.

A pool's `concurrency` limits its active managed executions. `reserved` holds slots
for that pool; unused reservations are not borrowed. Reservations cannot exceed
global capacity. Policy changes affect new admissions and do not preempt or stop
running work. Consequently a newly increased reservation becomes available as
existing executions finish. Ownership conflicts remain an additional constraint:
a ready task can wait for a path/resource claim without consuming an execution
slot. Dependency-blocked tasks do not acquire claims.

A task deadline includes queue time and caps the existing per-attempt time limit.
Queued work whose deadline expires is failed without preparing a workspace or
starting a command. Checkpoint recovery preserves the original execution deadline;
retrying a task does not extend its absolute task deadline. This is separate from
the optional deadline-based selection discipline.

## Durability and current limits

Task snapshots and named `task_effect` transitions are included in the same synced
Effects that change run lifecycle. Completion publishes task completion with the
terminal run event, avoiding a window in which run readers see success but the
task remains active. Rewind Decisions express intent; queue/admission/restore
Effects update task state. Standalone `pool_configured` Effects share the same
hash chain, and the offline metric/audit readers verify them before reading run
streams. Unchanged blocked state and identical pool configurations do not append
repeated events.

Historical records receive a labelled `legacy_projection` during replay without
rewriting the log or inventing old scheduling decisions. Old-attempt audit events
cannot overwrite the task's current attempt. Pool policies, admission witnesses,
turn sequences, deadlines, and lineage survive restart. Claims left uncertain by
a crash still require the inspection workflow in [FLEET.md](FLEET.md).

This implements the local Task/drainer substrate of `06`, not the full Work Pool
plane. Still missing: SS/FF/SF edges, typed/generalized data-result transport, arbitrary result
predicates, graph edits and atomic batch intake, weighted-fair/random/agent/custom
selectors, preemption, generalized sources, governed invoke and continuation,
services/connectors/sinks, sibling patch integration, and distributed tenant
capacity/budget enforcement. There is no model-driven selection by default and no
new supervisor or isolation authority granted by these scheduling records.
