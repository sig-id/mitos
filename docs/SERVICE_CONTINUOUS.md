# Continuous services

An active continuous unit maintains one managed Task. It starts on activation,
waits for that Task to finish, and creates a fresh Task after `restart_delay`:

```json
{
  "id": "worker",
  "scope": "project",
  "trigger": "continuous",
  "schedule": {
    "type": "continuous",
    "restart_delay": 5
  }
}
```

Use the normal [service catalog](SERVICES.md) with an installed-application work
binding and desired activation. Delay is in seconds, defaults to 5, and accepts
0–31,536,000. Zero still uses the dispatcher tick; it does not run a synchronous
completion loop. Interval, cron, timezone, jitter, event patterns, and
`run_on_startup` cannot be combined with this schedule.

## Managed lifetime

Activation records an immediately due controller in the catalog Effect. Within
the existing 200 ms dispatcher cadence, admission creates its Task and Run receipt
with the controller's ownership, epoch, and generation in one synced Effect.
A queued receipt already owns the generation. Dependency waits, claims, admission
limits, parked decisions, and recovery do not cause a second managed Task.

The controller tracks Task completion, including success, final failure, and
cancellation. The terminal Task Effect records the next due time using the saved
schedule's delay. Intermediate service retries retain the same Task and controller
ownership. Retry backoff therefore finishes before continuous restart delay starts.
The next generation has a fresh Task, Run, and workspace; it does not reuse the
previous worker's result tree. Each generation receives its work Brief's timeout.
An explicit absolute deadline remains fixed across generations and prevents new
intake after it expires.

This is one automatic managed Task per unit. `max_instances` still bounds all work
from that service; manual triggers can use additional configured capacity. Those
manual Tasks retain the ordinary manual lifetime. The controller does not fill
all `max_instances` slots with daemon copies.

## Stop, replace, and recover

Cancelling a managed Task stops that execution and preserves its evidence. If the
service remains active, the controller starts a replacement after the delay.
**Disable the service to stop its managed work.** The run page explains this and
links to Services. Completed controller Tasks do not offer the ordinary Run again
button; service activation controls new generations.

Disabling, removing, changing trigger type, or changing a continuous schedule/work
binding retires the current managed Task. The saved configuration fences queued
old work before claims. Reconciliation then records a stop request, terminates
owned execution, and preserves bounded harvest evidence. A pending service retry
is closed as part of retirement. When the new configuration remains continuous
and active, replacement waits for retirement to finish and then observes the new
restart delay. Reactivation without outstanding work starts immediately.
Unrelated catalog edits preserve ownership, epoch, generation, and due time.

Server restart replays the recorded controller. A future due time is retained; an
overdue controller submits at most one Task, with no downtime backlog. A committed
receipt is never submitted again. Interrupted running work is parked through the
existing recovery contract and retains its generation. It needs the same operator
recovery and crash-held-claim inspection as other local work. An active controller
does not silently replace an interrupted process whose outcome is unknown.

Old terminal generations cannot be resurrected through manual retry after the
controller has relinquished their ownership. Accepted recovery of the current
parked Task and configured retries remain available within that generation.

## Admission and operator evidence

Pool, dependency, service concurrency, ownership, circuit breaker, abort, and
provider rules apply to every generation. Capacity, breaker, and abort blocks
retain the due generation and record a reason; unchanged blocks do not generate
repeated status Effects. Breaker cooldown and policy changes can release the
block. A rejected work binding, unavailable application/workspace, expired
absolute deadline, or invalid scheduling request pauses automatic intake. Fix the
binding and change its work/schedule, or deactivate/reactivate, to resume. An
unchanged catalog save does not reset a paused controller.

The Services UI/API expose `continuous` state: `epoch`, `generation`, `task_id`,
`status`, optional `next_at`, and a reason. States are waiting, running (including
queued/parked managed work), retiring, blocked, rejected, or disabled. A managed
Task link opens its row on the Tasks page. Removed units retain retirement state
in the log even though they no longer appear in the catalog view. The Task API
records `continuous_epoch`; retries retain it. CLI service views use the same DTO.

Final success/failure outcomes also participate in [service outcome chaining](SERVICE_EVENTS.md).
Cancellation does not emit those outcome names. Atomic controller transitions and
service provenance are understood by the verified live/offline metric readers.

Continuous execution keeps the same local process provider, bounded per-Task
execution, and zero unattended grant. Readiness gates, process-independent
stop/restart propagation, distributed ownership, custom event names and provider-specific ingress connectors, hosted
tenancy, and the broader product gates remain outstanding.
