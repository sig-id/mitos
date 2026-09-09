# Service outcome chaining

Active event services create Tasks when another service Task finishes. Configure
an event schedule in the normal [service catalog](SERVICES.md):

```json
{
  "id": "publish-report",
  "scope": "project",
  "trigger": "event",
  "schedule": {
    "type": "event",
    "trigger": "service.succeeded:build-report"
  }
}
```

`schedule.trigger` accepts one string or an array of strings. Each service needs
its ordinary installed-application work binding and desired activation. An event
schedule has no interval, cron, timezone, jitter, or startup occurrence.

The equivalent hook form infers `trigger: "event"` when omitted:

```json
{
  "id": "publish-report",
  "scope": "project",
  "triggers": {
    "on_complete": ["build-report"],
    "on_failure": ["validate-report"],
    "on_finish": ["audit-report"]
  }
}
```

Choose hooks or a schedule. A unit accepts 1–32 patterns before deduplication.
Hooks take exact service IDs. Schedule patterns accept exact IDs or one trailing
wildcard, such as `service.completed:*` or `service.failed:build-*`. Interior
wildcards are refused. Custom names and `*` are also supported; see
[custom service events](SERVICE_CUSTOM_EVENTS.md).

## Outcome semantics

| Pattern | Hook | Source Task outcome |
|---|---|---|
| `service.succeeded:<id>` | `on_complete` | `done` |
| `service.failed:<id>` | `on_failure` | `failed` |
| `service.completed:<id>` | `on_finish` | Either of the above |

These are final **Task** outcomes. A failed Run with a pending service retry keeps
its Task nonterminal and emits no failure event. Exhaustion or closure of that
retry can produce the final failure. A Task that fails before executing can also
emit failure. Cancellation emits none of these names. Labelled sample Tasks do
not emit them.

Each outcome has a success/failure name and a completed alias. A recipient gets
one delivery for that outcome even when multiple patterns or hooks match both
names. Matching subscriptions must be active when the source outcome is saved.
Enabling a subscription later does not consume historical outcomes. Service
removal does not prevent an already submitted source Task from emitting its final
outcome to other active subscribers.

## Durable delivery and provenance

The terminal Task Effect includes the matched recipients, catalog revision,
normalized patterns, matching names, frozen work Briefs, queue priority, and allocation outcomes.
Pending delivery therefore survives a crash between source completion and child
submission. Creating the child Task and Run receipt consumes that delivery in the
same synced Effect. Restart and concurrent reconciliation do not create a second
receipt for the same source Effect and recipient.

The child Task's `service_event` records the source Effect sequence and hash,
source service/Task/Run IDs, both outcome names, causal root sequence/hash, and
chain depth. The worker receives this object on JSON stdin. Automatic retries
retain the same cause and root. This metadata does not copy source artifacts;
artifact transfer still requires explicit [data dependencies](DATA_DEPENDENCIES.md).

Before submission, the engine revalidates the installed application, workspace,
deadline, and scheduling request. It suppresses a pending delivery if its service
is inactive or its normalized subscription or work binding changed. Unrelated
catalog edits preserve delivery. No queued receipt is changed by a later edit.
There is no separate pending-delivery cancellation endpoint; disable the
subscription to suppress pending intake, or cancel an existing Run normally.

## Capacity and feedback limits

Pending deliveries reserve service queue capacity immediately. A full queue,
abort latch, or unavailable breaker records a skipped outcome with a reason.
They are checked again as appropriate at consumption and physical admission.
Source completion releases its slot before allocating a self-subscription, and
its breaker/retry/abort changes participate in the same allocation decision.
Existing pool, dependency, claim, deadline, and service concurrency rules govern
the child execution. A reservation remains owned if the concurrency limit is
lowered before consumption; current limits still gate execution.

Feedback is bounded by two persisted limits:

- At most eight event-delivery generations after a manually or temporally
  triggered root. A generation-eight Task can finish but cannot allocate another
  generation.
- At most 64 allocated descendant deliveries across all branches of one root.
  Reservations spend this budget even if subsequently suppressed or rejected;
  the budget is not refunded. Automatic retries keep the original lineage.

Recipients are allocated in service-ID order. The dispatcher drains at most 64
pending deliveries per pass, ordered by source sequence then recipient ID. The
remaining records wait durably for a later pass. A skipped allocation does not
automatically retry the source event.

## Operator evidence and boundary

The Services page and API show pending, submitted, skipped, and rejected counts,
plus the latest delivery's source Run, outcome, and reason. Suppression contributes
to the skipped count. Submitted means a Task receipt exists; it does not imply
successful execution. The canonical event log retains each allocation and
consumption. Live and offline metrics treat child Tasks as ordinary work.

The three registered service outcome families and separately emitted
[custom names](SERVICE_CUSTOM_EVENTS.md) are supported. Raw `worker.completed`
and `scheduling_event` producer integration, readiness gates, and distributed fencing remain
outstanding. Chaining retains the local provider's zero unattended grant and does
not implement hosted tenant isolation or satisfy either full product launch gate.

[Webhooks and external signals](SERVICE_INGRESS.md) use separate delivery intake
with bounded payloads and delivery-key idempotency; they are not additional event
subscription names.
