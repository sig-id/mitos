# Service circuit breakers

Manual, interval, cron, event, and continuous service units can stop admitting new executions after repeated
failures. Configure a breaker on a catalog unit:

```json
"circuit_breaker": {
  "enabled": true,
  "threshold": 5,
  "cooldown": 300,
  "half_open_requests": 1
}
```

The field names retain the service specification's semantics. Breakers default to
disabled. Defaults when enabled are five consecutive failures, 300 seconds of
cooldown, and one recovery probe. Supported ranges are 1–1000 failures, 1–31536000
seconds, and 1–32 recovery probes. Service concurrency and pool limits still apply;
probes can execute sequentially when those limits are below the probe count.

## State and admission

A **closed** circuit permits ordinary execution. Each admitted Run's first terminal
`failed` Effect counts as a failure; a `done` result resets consecutive failures.
This includes execution timeouts and failed acceptance that finish as `failed`.
Queued admission/deadline rejection, skips, and parked work are not completed
executions and do not count as breaker failures. Cancellation is neutral.

At the threshold, the circuit becomes **open**, with a persisted cooldown deadline.
New manual keys return a conflict; existing keys still return their original
receipts. Due interval occurrences record skips and advance their schedule cursor.
Already queued Tasks remain queued/blocked and cannot acquire claims. Their wait
reason is `service-breaker:<service-id>`. Already admitted work can finish or
continue checkpoint recovery; opening the breaker does not kill it.

After cooldown, the next eligible admission starts **half-open** recovery. Work
from the previous generation must drain before the first new probe. Probe quota
is reserved atomically with admission, before execution, and rechecked under the
Store lock after pool selection. Passing all configured probes closes the circuit
and resets failures. One failed probe reopens it with a fresh cooldown. Until a
probe finishes, its reservation prevents quota reuse. A parked probe retains that
reservation; cancellation releases it for a replacement. An accepted retry of a
parked probe releases the old reservation when that attempt is closed, and the new
attempt must pass normal admission again.

Cooldown expiry itself does not start a process or emit a speculative recovery
success. Pool/dependency/service capacity still determines when an actual probe
can start. Manual services need a trigger or queued Task; interval services also
need a due occurrence or queued Task.

## Persistence and policy changes

Admission Effects record the service ID and breaker policy epoch/generation.
Terminal Effects carry the resulting circuit snapshot alongside the Run result.
There is no post-completion update window: readers see completion and protection
together after fsync. Replay rebuilds the exact cooldown, probe reservations, and
failure count. A crash while a probe was running follows ordinary parked-run
recovery and retains its probe reservation.

Opening or closing a circuit advances its generation. Late outcomes from older
generations cannot close or reopen the current circuit. This prevents an earlier
concurrent success from undoing a newer failure gate. Such outcomes remain in the
Run history but do not alter the current breaker counters.

Unrelated catalog edits, disabling/reactivating desired service activation, and
server restarts preserve the breaker. Changing its effective threshold/cooldown/
probe policy creates a new policy epoch and closed state; earlier admitted outcomes
cannot modify that new epoch. Explicitly disabling the breaker or removing its
unit removes this admission gate. Re-enabling it starts a new policy epoch. These
changes are saved through the existing revision-checked catalog operation.

## Operator evidence and boundaries

The Services CLI/API and console expose circuit state, consecutive failures,
recovery deadline, and probe progress. The event log retains admission bindings and
terminal snapshots. Live autonomy and verified offline readers understand these
records without counting breaker state changes as human decisions or extra work.

Breakers are per service in the existing local project. They bound failure-driven
execution; they do not provide hosted tenant isolation or a
positive unattended grant. [Service restart/backoff policies](SERVICE_RESTARTS.md)
use these same breaker gates. Those
remaining mechanisms and the full product gates are tracked in the implementation
status. See [service composition and scheduling](SERVICES.md) for the catalog,
interval clocks, queue bounds, and manual idempotency contract.
