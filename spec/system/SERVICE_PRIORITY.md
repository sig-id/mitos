# Service queue priority

Status: Normative local behavior and API contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

A unit can set its queue preference in the existing concurrency object:

```json
{
  "id": "report",
  "scope": "project",
  "trigger": "manual",
  "concurrency": {
    "max_instances": 1,
    "if_running": "queue",
    "queue_max": 10,
    "priority": "high"
  }
}
```

The registered values are `low`, `normal`, `high`, and `critical`. Omission means
normal. Other strings, including case variants, are refused by catalog preview
and save. The field applies to all registered service triggers.

## Relationship to pool policy

Service priority adds a fixed adjustment to the Task's numeric scheduling
priority. The original Brief priority remains unchanged:

| Service priority | Saved adjustment |
|---|---:|
| low | -100 |
| normal | 0 |
| high | 100 |
| critical | 200 |

These are fixed v1 score points. In a `priority` pool, the selection score is:

`Task priority + service adjustment + whole ready minutes + satisfied soft dependencies`

The Task's base priority still accepts -1000 through 1000. The combined selection
score can exceed that range; it is not clamped. Human Tasks and historical service
Tasks with no binding have zero adjustment. For otherwise equal work, critical
precedes high, then normal, then low. Existing aging remains effective: a low Task
that has been ready for 301 minutes outranks a freshly ready critical Task with
the same base priority and dependency score.

The pool retains its selected discipline. FIFO orders by ready time, LIFO by
reverse ready time, and EDF by deadline. Named service priority does not change
those policies or the rotation between pools. The default interactive pool uses
priority; the default batch pool uses FIFO. To use priority ordering in batch,
configure that pool's discipline through the existing pool controls.

Priority never makes an unsatisfied dependency ready, bypasses claims, increases
capacity, releases a breaker/abort block, or preempts admitted work. New arrivals
participate in the next ready-set snapshot. Deterministic ready-time/ID ties and
pool-policy rechecks at admission remain unchanged.

## Binding and replay

Manual, interval, cron, and continuous intake save a queue binding with Task/Run
creation. The binding records service ID, catalog revision, priority name, and
numeric adjustment. An event subscription saves it earlier, with its pending
delivery allocation, because that allocation already reserves queue capacity.
Consumption preserves the allocation preference even after a priority-only
catalog edit. Its call records the current intake revision separately from the
binding's original allocation revision.

Changing service priority affects future intake and future event allocations.
Existing queued Tasks keep their saved preference. Automatic retries and current
parked recovery keep the Task binding. Repeating a manual idempotency key returns
the original receipt and preference. A new continuous generation uses the current
service priority without retiring the currently managed Task.

Historical records without a queue binding remain valid. Their adjustment stays
zero, including a pending event delivery consumed after the upgrade; it does not
acquire a new class from an edited catalog. Replay uses saved Task bindings and
does not recalculate them from current configuration.

Services UI/API/CLI show the configured priority for future intake. Tasks UI/API
show their bound service priority separately from base Task priority. The
`service_queue` object contains the complete binding, also recorded as `queue` in
the creation call and event recipient evidence. Live/offline metrics continue to
measure service Tasks normally.

This is queue selection in the current local project. Preemption,
readiness gates, distributed priority/fencing, hosted tenant allocation, and the
remaining product mechanisms are still outstanding. The local provider's zero
unattended grant is unchanged. See [services](SERVICES.md) and
[work pools](WORK_POOLS.md) for their admission and capacity contracts.
