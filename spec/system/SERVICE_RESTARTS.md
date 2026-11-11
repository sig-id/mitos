# Service restart policies

Status: Normative local behavior and API contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

Service units support the retained `restart_policy` fields:

```json
"restart_policy": {
  "on_failure": "retry",
  "max_retries": 2,
  "backoff": {"initial": 5, "multiplier": 2, "max": 300}
}
```

`on_failure` is `skip` (default), `retry`, or `abort`. The retry limit defaults to
2; explicit zero means no automatic retries. The limit counts additional attempts
for one triggered Task, not future independently triggered Tasks. Supported bounds
are 0–32 retries, 1–86400 seconds initial delay, a multiplier from 1 through 10, and
a maximum delay from the initial value through 31536000 seconds. Fractional
multipliers are supported. Delay for retry N is
`min(max, initial * multiplier^(N-1))`, in seconds.

## Retry lifecycle

Service admission binds the effective policy, root Run, attempt count, and retry
deadline to the Run in its admission Effect. The retry window starts at the first
service admission and is capped by its timeout and any explicit Task deadline.
Each automatic child retains this deadline in its Brief and Task scheduling;
queueing, breaker cooldowns, backoff, and execution all consume that same window.
Automatic retry never extends it.

An admitted attempt's first terminal failure evaluates that binding. `retry`
records the next attempt number and due time in the same Effect as failure. A
backoff reaching the deadline closes the plan as `expired`; reaching the configured
count closes it as `exhausted`. `skip` leaves the Task failed without retrying.
Queued rejection, cancellation, parking, and uncertain execution after a crash do
not automatically create a new service attempt. Checkpoint recovery within an
admitted Run continues to use its own existing supervision/decision rules.

While an automatic retry is pending, the Run retains its failed result and the
Task remains `blocked`, with `waiting_for: ["service-retry:<run-id>"]`. This keeps
failure-dependent Tasks blocked until retries are exhausted or closed. The pending
retry reserves one outstanding service slot, preventing interval/manual intake
from filling its place during backoff.

When due, the engine revalidates the saved Brief and current installed application,
workspace, and Task scheduling. It creates a fresh Run under the same Task identity
and consumes the retry plan in that creation Effect. The child uses ordinary Task
dependency, pool, ownership, service concurrency, and circuit-breaker admission.
Prerequisites are rebound at admission. Backoff expiry itself grants no permission
to bypass those gates. The child is a new copied-workspace attempt; prior artifacts
and execution records remain preserved.

A successful child completes the Task. Exhaustion, expiry, or refused retry
admission leaves it failed, allowing matching failure guards to proceed. A due
retry can also wait behind an open circuit until the original deadline expires.
An independently scheduled future occurrence can still create another Task after
this lineage ends; use a circuit breaker or `abort` to stop repeated new failures.

## Cancellation and configuration changes

The Services screen lists pending retries, their due times/deadlines, and a
**Cancel retry** action. It uses the existing `POST /runs/{id}/cancel` operation.
Cancellation persists a closed plan and cancelled Task together; the failed Run
and artifacts remain evidence of the attempt. Repeated cancellation is idempotent.
Cancelling a child that already exists uses the ordinary child Run stop operation.

A manually requested retry can supersede a pending automatic retry. The engine
then closes the old plan without creating another child or overwriting the newer
Task attempt. Changes to activation or effective restart policy suppress pending
plans. Changes to the saved work binding do not rewrite an existing Task's Brief.
A missing app/workspace or failed admission closes a due plan with its reason.
Extending an absolute Task deadline requires new work, rather than automatic retry.

## Abort behavior

`abort` records a service stop with the failure Effect. It withholds new manual
keys, scheduled occurrences, and queued service execution; existing receipt keys
remain readable. Other services continue. Already admitted execution/checkpoint
recovery can finish under its original rules. Abort state survives unrelated
catalog edits and restart.

Explicit deactivation followed by reactivation clears the stop. Changing the
effective restart policy or removing the unit also clears it. A failure from an
attempt bound to an older, changed policy cannot impose that old policy's abort
on the new configuration. This is local per-service failure handling; it is not
a global Studio shutdown or pipeline lifecycle propagation.

## Persistence and scope

All bindings, failure plans, closures, and child receipts are synced Effects in
the event log. Startup reconstructs pending backoff and its original deadline.
Concurrent reconciliation cannot consume a plan twice, and a crash between
failure and child creation cannot lose the pending plan. A crash after child
creation replays that same child. Console/CLI/API readers share this state, and
live/offline autonomy folds continue to agree.

Service runtime readers show pending retries and abort state, and link to the
latest attempt of the latest triggered Task. Historical retry outcomes remain in
Run event history. Retries retain the existing local provider posture and do not
raise the unattended grant. Readiness gates, remaining triggers,
lifecycle propagation, hosted tenancy, and the complete product gates remain
outstanding. See [services](SERVICES.md) and [circuit breakers](SERVICE_BREAKERS.md).

[Service health checks](SERVICE_HEALTH.md) can also request bounded retries when
ordinary failure policy is `skip`. These plans record `cause: health` and preserve
the same retry budget and deadline. A health `kill` suppresses ordinary failure
retry; explicit `abort` still takes precedence. Changed health policy suppresses
pending health retries. Health failures bypass checkpoint restart supervision.
