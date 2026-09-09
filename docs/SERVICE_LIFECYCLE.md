# Service stop, restart, and resume

An explicit service stop suspends new work and requests shutdown of existing work.
It applies to a unit or target and recursively to units/targets whose `part_of`
relations name an affected member. It leaves the configured activation intent
intact. A separate resume operation releases the stop after shutdown settles.

```json
{"id":"listener","scope":"project","trigger":"webhook",
 "part_of":["endpoint.target"],
 "schedule":{"type":"webhook","path":"/capabilities/listener/runs"}}
```

Declare `endpoint.target` in the catalog's targets and give `listener` its usual
work binding and activation. `part_of` does not activate either side. `requires`
and `wants` alone do not add a member to these explicit stop operations. Overlapping
membership and cycles visit each affected node once; propagation is from a named
parent to its members, never from a child to all its parents.

## Commands and UI

```sh
mitos service-stop --key maintenance-42 endpoint.target
mitos service-lifecycle
mitos service-lifecycle 42
mitos service-resume --key resume-42 endpoint.target
```

The detail command reads operation sequence 42. `--url` selects a nondefault
loopback port. Stop and resume require a bounded nonempty idempotency key. The
Services page provides the same actions for units and targets, shows current
holds per unit, and links recent operations to their affected work.

```http
POST /api/services/endpoint.target/lifecycle
Content-Type: application/json

{"action":"stop","idempotency_key":"maintenance-42"}
```

Use `action: "resume"` with a new resume key to release the root's settled stop.
`GET /api/service-lifecycle` returns the latest 50 operations, newest first.
`GET /api/service-lifecycle/{seq}` reads one operation. Each view includes its root,
original catalog revision, membership/provenance, request/update times, status,
total affected Task count, and the first 50 Task/Run/head witnesses. `more_tasks`
signals that additional affected Tasks are omitted from the view. The operation
still covers the entire captured cohort.

Successful requests return HTTP 200 with the durable receipt. `stopping` means
shutdown is requested; `stopped` means the captured work, active worker bookkeeping,
execution claims, and pending deliveries have settled. `resumed` means this stop's
holds were released. Resume while still stopping returns 409. Unknown roots or
operations return 404; malformed action envelopes return 400. Existing local Host
and cross-origin mutation protection applies.

## Durable scope and shutdown

`effect/service_stop_requested` saves the resolved `part_of` scope and catalog
revision while installing all admission holds under the Store writer lock. It
also records the active local Task identities needed to account for terminal
workers that have not yet exited. At this exact event boundary, application and
replay derive the same affected Task cohort and original Task-head witnesses:
nonterminal member Tasks, member Tasks with unreleased claims, and still-active
member workers. This avoids copying an unbounded Task cohort into one log record.

New manual/timed/continuous/event/webhook/signal intake is blocked. Direct claim
acquisition, queued dispatch, and new attempts of held service Tasks are also
blocked. Existing idempotent delivery/trigger receipts remain readable. Admission
refusals do not consume a new external delivery key. Conditions, acceptance, and
the original work binding retain their existing meanings.

Reconciliation suppresses pending event allocations and uses the normal durable
Run cancellation path for captured work. Shutdown requests visit deeper captured
members first, with at most 64 cancellation/delivery actions per pass. Already
stopping Runs are awaited rather than signalled repeatedly. Failed attempts with
pending retries have their retry plans cancelled. A Task that finishes naturally
before cancellation keeps its recorded outcome.

Live command shutdown preserves output and harvests workspace changes before
completion. `effect/service_stop_settled` is saved only after all captured Tasks
are terminal, their worker entries have exited, all their attempt claims are
released, and affected pending deliveries are closed. A crash cannot establish
that an orphaned local process has stopped: unreleased claims continue blocking
settlement and resume until the existing explicit inspection/release workflow.
Startup resumes pending stop reconciliation before service intake and dispatch.

## Resume and overlapping stops

At most one unresumed stop may originate from a root. Different roots can hold
the same member independently. Resuming one releases only its own holds; a second
root's stop continues blocking the member. Catalog edits, removal of the root,
or membership changes do not discard a captured stop or prevent its later release.
Newly declared members are not added retroactively to an existing operation.
Existing held identifiers remain held if removed and reintroduced.

Keys are scoped by root and action for the lifetime of the log. Duplicate stop
and resume requests return their original operation with current progress, even
after resume, removal, or restart. Reusing an old stop key never starts another
shutdown. Use a new key for a new operation.

Resume restores ordinary trigger behavior; it does not resurrect cancelled Tasks,
reissue suppressed events, clear breakers/abort policy, change catalog activation,
or start an extra manual invocation. Timed occurrences skipped during the stop
advance the normal cursor and are not replayed. Continuous work uses its saved
next-generation timing. Manual and ingress services wait for fresh requests.

## Restart

`mitos service-restart --key KEY ROOT` or the lifecycle API's `action: "restart"`
uses the same captured `part_of` stop scope. It creates one fresh Task for each
active unit in that scope, preserving configured activation. Inactive units are
not enabled. Completion prerequisites must also be active members of the restart
group; otherwise the request is refused before stopping anything.

The request records its restart identity and replaces an existing settled stop or
failed restart of the same root atomically. The old operation becomes `superseded`.
After old workers, claims, and deliveries settle, the operation becomes `starting`.
Fresh receipts are prepared in passes of at most 64, each bound atomically to the
request sequence/hash/root and one shared completion cycle. Their absolute deadlines
are derived from request time and each work binding's timeout, capped by an existing
absolute deadline. Shutdown and other-hold waits consume that budget.

The operation's hold blocks execution and outside intake throughout preparation.
Only its own planned receipt creation can pass this hold. `service_restart_completed`
releases the group in one synced Effect after all receipts exist and execution
policies/work bindings are revalidated. Status `restarted` means the group was
published, not that its applications succeeded. Continuous units retain controller
ownership of the fresh generation. Other trigger kinds receive one explicit fresh
invocation, then continue following their normal triggers.

Independent stop holds remain in force. Overlapping restart operations are refused
to avoid mutual waits. Policy/work/activation changes, unavailable applications,
expired deadlines, cancelled prepared Tasks, or withheld breaker/abort admission
cancel the prepared cohort before execution and leave `restart_failed` with the
hold intact. `service-stop` during a pending restart cancels that restart and also
leaves the group stopped. Resume releases a settled failure; a new restart key can
replace it. No prepared cancelled Task is resurrected.

Restart receipts, frozen bindings, and progress replay from the source catalog and
atomic child records. Restart keys are scoped independently of stop/resume keys.
Task JSON and command stdin expose `service_lifecycle` with the source sequence,
hash, and root; retries preserve it. The lifecycle view lists planned fresh work,
deadlines, and created Run links, and Run pages link back to their restart source.

## Remaining lifecycle work

Automatic propagation from desired deactivation/reconfiguration,
generalized readiness and start-order barriers, pipeline activation roots,
distributed fencing, and hosted tenancy remain separate missing mechanisms.
It preserves the local provider's zero unattended grant and does not establish
either full product launch gate.
