# Replacing overlapping service work

Set `concurrency.if_running` to `kill` to replace an overlapping instance when a
new service occurrence arrives at capacity:

```json
{
  "id": "worker",
  "scope": "project",
  "trigger": "manual",
  "concurrency": {
    "max_instances": 1,
    "if_running": "kill"
  }
}
```

The field applies to manual, interval, cron, event, continuous, webhook, and external-signal intake. Below
`max_instances`, intake creates ordinary work. At capacity, a replacement captures
one prior Task and queues a new Task while requesting that prior work stop.
This uses the existing bounded stop, process-group termination, and harvest path;
the new Task cannot execute while the old worker is still stopping.

## Selection and bounded intake

Capacity counts outstanding service Tasks, including queued work, parked work,
pending retries, and event-delivery reservations. Selection prefers an admitted
Task over one that has only queued. Within that group, it selects the oldest Task
by creation time, breaking ties by Task ID. A queued Task is eligible if there is
no admitted candidate. A stop already in progress is allowed to finish.

There is at most one unsettled replacement per service, and at most one extra
receipt/reservation above `max_instances` during that stop. Further manual keys
receive a capacity conflict; timed/event occurrences record a skip. A refused
manual key is not reserved. Existing keys return their original receipt. Once the
prior work settles, another occurrence can replace outstanding work normally.

`queue_max` is used by queue mode, not as an additional replacement backlog. If a
catalog change lowers the limit below an already accepted backlog, intake waits
for that backlog to drain within the new limit. It does not discard multiple
accepted Tasks to make space for one arrival. A service with several slots
replaces one instance, retaining the other work.

Application, workspace, deadline, scheduling, breaker, and abort admission run
before accepting a replacement. An invalid occurrence creates no stop request.
Priority affects queue selection after readiness; it cannot bypass shutdown.

## Durable target and stop

The replacement records its service/catalog revision, prior Task ID, prior current
Run ID, and that Task's exact sequence/hash witness. Manual and timed intake save
this target with the new Task/Run receipt. An event subscription saves it with
pending delivery allocation and preserves it at consumption. The target never
means “whatever happens to be running under this service ID later.”

Pending event allocation fences the targeted Task's queued admission and retry.
It does not yet stop the worker. Consumption revalidates the subscription and
work before creating a replacement receipt. Deactivation, changed subscription or
work, or changing away from kill mode suppresses the pending delivery and releases
its fence. Once a receipt is committed, its stop intent remains attached to that
Task across unrelated catalog changes.

The reconciler records the normal stop Decision and cancellation Effects against
the target Task's current attempt. It also closes pending service retries.
Repeated reconciliation, duplicate trigger keys, and replay do not issue another
stop for an already stopping/stopped attempt. A derived reference-count index
reconstructs targeted-Task fences from receipt and delivery Effects.

The replacement remains blocked until the prior Task is terminal, every prior
attempt's execution claims are released, and the engine no longer holds an active
worker handle for that Task. Shutdown and bounded harvest finish before that
worker handle is removed. Uninspected crash-held claims remain held and require
the existing operator inspection; replacement never supplies that confirmation.
Ordinary dependencies, pool limits, service concurrency, breaker/abort, and
ownership rules still gate the eventual admission.

## Cancellation and lifetime changes

Cancelling the replacement before its stop is requested withdraws that pending
intent and releases the old Task's fence. It cannot undo a stop already recorded
against the old attempt. A replacement whose absolute queue deadline has expired
does not initiate a new stop; normal deadline reconciliation closes its Task.

Continuous services retain their separate managed lifetime. If a newly allocated
continuous replacement is retired by a configuration change before its stop is
requested, it does not stop independent manual work it had targeted. Conversely,
when a manual occurrence replaces a continuous managed Task, desired activation
can start a fresh managed generation after its configured delay; the same capacity
and overlap policy applies to that later occurrence. Disable continuous activation
when the desired outcome is to stop its managed lifetime.

While a live replacement owns the stop intent, manually retrying its targeted old
Task is refused. The replacement itself keeps the target binding through its own
retries. Cancellation of the old Task emits no service success/failure completion
names; the new Task emits its own final outcome normally.

## Operator evidence and boundary

Task API responses include `service_replacement`; creation calls and event
recipients include `replacement`. Services UI/API/CLI show live replacement
receipts with links to prior work and whether its recorded stop/claim release is
still pending. Engine admission additionally checks active worker handles.
Queued Task wait reasons distinguish `service-superseded:<id>` from
`service-replacement:<id>`. Run pages link directly to the saved prior Run.
The log retains stop Decisions, harvested artifacts, claim release, and admission
ordering; live/offline metric readers understand the bindings.

This is replacement within one local service, not cross-pool preemption or a
provider-wide kill API. Distributed fencing, readiness gates, related-unit
lifecycle propagation, provider-specific ingress connectors, and hosted tenant control remain
outstanding. It retains the local provider's zero unattended grant. See
[services](SERVICES.md), [continuous work](SERVICE_CONTINUOUS.md), and
[work pools](WORK_POOLS.md) for their surrounding contracts.
