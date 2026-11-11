# Service completion dependencies

Status: Normative local behavior and API contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

Units can declare the retained `depends_on` relation:

```json
{"id":"publish","scope":"project","trigger":"manual",
 "depends_on":["sync","usage"]}
```

All named prerequisite services must complete successfully in the same scheduler
cycle before this Task can acquire claims or run. This is independent of
activation `requires`/`wants`, start ordering `before`/`after`, and explicit Task
control/data dependencies. It does not enable or trigger a prerequisite and does
not weaken the Task's configured all/any/quorum join.

The catalog validates every reference against a unit and rejects duplicate names,
self-dependencies, missing units, and dependency cycles. Targets are not runtime
completion prerequisites. A unit has at most 128 prerequisite names. Inactive
prerequisites remain valid declarations but cannot supply new successful work
until explicitly activated and triggered in the matching cycle.

## What constitutes a cycle

Every new service Task records `service_cycle`: an ID, catalog revision, service
ID, and frozen prerequisite names. A cycle is scoped by both ID and catalog
revision and admits at most one Task per service. Retries are attempts of that
same Task, not new cycle members. A success from another cycle, another catalog
revision, a sample, or an unrelated later Task never satisfies the dependency.

| Intake | Shared cycle identity |
|---|---|
| Explicit manual triggers | A caller-supplied cycle key and catalog revision |
| Interval and cron occurrences | The timestamp of the dispatcher pass that submits the due occurrences |
| Continuous generations | The same dispatcher-pass identity as timed occurrences submitted in that pass |
| Event recipients | The exact source outcome Effect sequence/hash and its allocation catalog revision |
| Webhook and external-signal deliveries | An explicit ingress cycle key and catalog revision; independent deliveries otherwise use standalone cycles |

For automatic schedules, a pass includes only occurrences that are actually due
and accepted by their existing intake gates. A dependency does not force an
otherwise-not-due service to run. Missed capacity, skipped conditions, parked work,
failed work, or a prerequisite absent from that pass cannot borrow a success from
a previous pass. A continuous service that never completes is not a successful
completion prerequisite; service readiness is a separate, still-missing contract.

Event prerequisites refer to sibling recipients of the same source outcome.
The source Task is not implicitly a member of the recipient cycle. Each sibling
receipt keeps its allocation revision even if delivery is delayed across an
unrelated catalog edit. Changing a pending recipient's dependency declaration
suppresses that pending delivery, alongside existing subscription/work checks.

## Manual triggers

Choose a new shared cycle key, then submit each member using its own trigger
idempotency key. Members can arrive in any order:

```sh
mitos service-run --cycle release-17 --key publish-17 publish
mitos service-run --cycle release-17 --key sync-17 sync
mitos service-run --cycle release-17 --key usage-17 usage
```

`publish` waits without acquiring an execution slot or claims until both
prerequisites succeed. The equivalent request is:

```http
POST /api/services/publish/trigger
Content-Type: application/json

{"idempotency_key":"publish-17","cycle":"release-17"}
```

The Services form exposes the same optional cycle key. A manual trigger for a
unit with dependencies requires an explicit cycle key; rejection creates no
receipt. Services without dependencies can use a private standalone cycle by
omitting the key. Existing trigger idempotency has precedence: repeating the
original service/key returns the original receipt, even when the supplied cycle
has changed. A different key cannot create a second Task for the same service in
one cycle. Use a new cycle for a new group of work.

Catalog revisions are exact cycle boundaries. If the catalog changes between
manual submissions, new members belong to the new revision even if the textual
cycle key is reused. Finish submitting a cohort before editing its catalog, or
start a fresh cycle. Existing receipts preserve their bindings through edits.

## Readiness, evidence, and deadlines

Waiting Tasks expose `service-dependency:SERVICE_ID` reasons and link to their
Run from the Tasks screen. The admission gate checks the exact cycle receipt index
and the prerequisite Task's current status. Only `done` satisfies it; a retry's
intermediate failed Run does not satisfy it while its Task remains blocked or
running. A successful later attempt of the same prerequisite Task can satisfy it.

The admission Effect atomically freezes `service_dependency_evidence` with each
prerequisite service ID, Task ID, successful Run ID, and Task-head sequence/hash.
The worker receives `service_cycle` and `service_dependencies` on stdin alongside
its ordinary Task dependency evidence. Once admitted, an invocation retains its
witnesses if a prerequisite is subsequently retried. A new retry attempt of the
dependent Task rechecks prerequisites at its admission; checkpoint recovery of
an already admitted invocation retains its original witnesses.

Dependent service Tasks receive an absolute deadline at receipt creation: the
shorter of their supplied deadline and `timeout_minutes` from that creation time.
Queueing and prerequisite waits consume this budget. An absent, failed, parked,
condition-skipped, or deactivated prerequisite therefore cannot strand an
unbounded dependent queue. The existing deadline path fails an expired queued
Task with evidence; normal service outcome handling observes that final failure.
Automatic retry retains the same Task/cycle and original deadline. A fresh trigger
in a new cycle is needed for an independent budget.

Service concurrency, replacement, abort, circuit-breaker, pool, explicit Task
joins, data dependencies, and ownership gates still apply after cycle prerequisites
succeed. Cycle dependencies do not grant permission to bypass any of these gates.

## Persistence and inspection

Cycle membership is saved with each Task receipt and rebuilt from the verified
event chain. No side file or “last successful service run” cache supplies authority.
The Run and Tasks pages show the cycle, and the Run page links successful
prerequisite evidence. `mitos task TASK_ID`, the existing Task APIs, worker stdin,
and verified offline metric readers expose the same records. The Services view
shows each declaration and the shared-key input for manual intake.

Startup retains membership and queued waits. Interrupted execution uses normal
park/recovery and claim-inspection rules. A crash partway through an automatic
pass does not merge later submissions into that earlier cycle: already-created
members retain their identity; a missing member leaves its dependent waiting
until its deadline or explicit cancellation. Historical receipts without cycle
metadata are not retroactively assigned prerequisites.

These are successful-completion dependencies within the current local service
runtime. Distributed cycle coordination, readiness gates, lifecycle propagation,
custom event names and provider-specific ingress connectors, and the broader product gates remain outstanding.
Cycle evidence retains the same local provider posture and zero unattended grant.
