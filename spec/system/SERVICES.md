# Local service composition

Status: Normative local behavior and API contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

The local Studio resolves a saved project catalog into desired activation and
creates durable Tasks from manual triggers, interval/cron schedules, service
outcome subscriptions, continuous controllers, webhooks, and external signals. The Services
screen (`/services`), CLI, and HTTP API use the same resolver and intake operations.
The project identifier is `local/studio`.

**Active manual services wait for a trigger; interval/cron services submit on
schedule; event services respond to matching Task outcomes and custom events; continuous services
maintain managed work; webhook and external-signal services accept deliveries.** Removing or disabling a service stops future occurrences and prevents
new manual trigger keys. Continuous controllers stop their managed Task when
disabled or removed. Other previously submitted Tasks retain their original Briefs
and continue through dispatch. Current service concurrency also applies to those
Tasks; removing a unit leaves a conservative one-instance limit for its remaining
Tasks.

## Catalog and resolution

A catalog contains `config` (units, targets, presets, and project intent), `work`
(one installed-application Brief per unit), and a server-assigned `revision`.
For example, save this as `services.json` with a workspace under the server root:

```json
{
  "config": {
    "units": [
      {"id": "inventory", "scope": "project", "trigger": "manual"}
    ],
    "targets": [
      {"id": "inspect.target", "scope": "project", "requires": ["inventory"]}
    ],
    "presets": [
      {"project_type": "local", "enable": [], "disable": []}
    ],
    "intent": {"project_type": "local", "targets": ["inspect.target"]}
  },
  "work": {
    "inventory": {
      "title": "Inspect the workspace",
      "workspace": ".",
      "app_id": "workspace-inventory",
      "timeout_minutes": 5,
      "scheduling": {"pool": "batch"}
    }
  }
}
```

The resolver applies these rules:

- Preset enables are optional roots. Preset disables suppress preset roots;
  target membership can still activate those units. Explicit project disables
  prevent activation from any source. Explicit enables and selected targets
  are mandatory roots.
- A root and its recursive `requires` closure are considered together. A missing
  or explicitly disabled requirement leaves the root `failed` and activates none
  of that bundle. Independently demanded members remain available.
- `wants` pulls optional bundles. Missing optional references produce issues.
  `conflicts_with` is symmetric: mandatory demands win over optional demands;
  two conflicting mandatory demands reject the whole configuration with their
  demander names. Optional conflicts are `masked`. Optional ties use deterministic
  identifier/root order, without attempting a globally maximal active set.
- `before` and `after` order active nodes without activating referenced nodes.
  Active ordering cycles reject the configuration. The reported order describes
  desired activation; it does not order separately submitted Tasks. Use Task
  dependencies when execution order matters.
- `part_of` propagates explicit [service stops](SERVICE_LIFECYCLE.md) through
  units and targets. Resume releases the captured holds independently of other
  roots. Restart publishes a fresh group after shutdown; automatic configuration-driven
  propagation remains outstanding.

Rows include state and activation reasons (`source`, `relation`, demand `strength`,
and `root`); issues explain missing requirements and masked bundles. This view is
computed from saved configuration, not stored as authoritative runtime state.
The catalog supports at most 256 units/targets, 32 presets, and 128 references per
relation list. The HTTP request limit is 32 KiB, including wrapper/form encoding.
Unknown JSON fields and unregistered scopes or triggers are refused.

## Preview, save, and trigger

```sh
mitos services-preview --file services.json
mitos services                         # read current revision and reasons
mitos services-configure --file services.json --expected-revision 0
mitos status --project local/studio
mitos service-run --key inspection-2026-09-09 inventory
```

Preview validates resolution, active installed-application bindings, workspaces,
and Task scheduling without writing events. Inactive bindings retain Brief schema
validation, so a service can be disabled after its app/workspace becomes unavailable
or its deadline expires. Reactivation revalidates the full binding. Saving requires the current catalog revision;
a stale revision returns a conflict. An unchanged normalized catalog writes no
new event. A changed catalog is synced as `effect/services_configured`, with its
event sequence as revision. Saved relative workspace paths become absolute.

Manual triggers require an idempotency key of 1–128 bytes, without surrounding
whitespace, NUL, CR, or LF. Repeating a `(service ID, key)` returns the original
Run receipt, including after deactivation, app removal, or server restart. Use a
new key for new work. Keys remain reserved for the lifetime of the event history.
The UI supplies a key per rendered trigger button.

A trigger revalidates its saved Brief, catalog revision, and service capacity, then syncs the Task,
Run receipt, and service call binding in the same creation Effect. Task `source`
is `trigger:<service-id>`; the call records the exact catalog revision. Pool policy,
dependencies, claims, deadlines, acceptance, cancellation, and recovery all use
ordinary Task/Run operations. A queued receipt is not an admitted execution.
Changing catalog configuration does not modify an existing receipt's Brief.

| Method | Endpoint | Body / response |
|---|---|---|
| GET | `/api/services` | Catalog, desired rows/order/issues, per-unit runtime state |
| POST | `/api/services/preview` | Catalog → validated preview |
| PUT | `/api/services` | `{"expected_revision": N, "catalog": {...}}` |
| POST | `/api/services/{id}/trigger` | `{"idempotency_key": "..."}` → Run receipt |

Store replay rebuilds configuration, Task source, and trigger deduplication from
the hash-verified log. The offline interruption/autonomy readers understand catalog
records and exclude them from run metrics; service Tasks remain ordinary measured
runs. Catalog activation itself does not imply work completion or a positive
unattended grant.

## Interval schedules and backpressure

An interval unit uses the existing schedule field names and seconds:

```json
{
  "id": "inventory",
  "scope": "project",
  "trigger": "interval",
  "schedule": {
    "type": "interval",
    "interval": 60,
    "jitter": 5,
    "run_on_startup": false
  },
  "concurrency": {
    "max_instances": 1,
    "if_running": "queue",
    "queue_max": 2
  }
}
```

Intervals are 1 second to 365 days; jitter is 0 through the interval in seconds.
The next occurrence is computed from the observed tick time plus the interval and
0–jitter seconds of deterministic spread. The dispatcher checks schedules every
200 ms; this is not a real-time scheduling guarantee. Saving an active schedule
initializes its clock in the catalog Effect. Unrelated catalog changes retain the
clock. Schedule changes and reactivation establish a fresh schedule epoch.

`run_on_startup` defaults to false. When true, activation is immediately due and
engine startup requests an immediate occurrence. This includes server restarts;
it still obeys service capacity. When false, restart preserves a future due time.
A past-due clock produces at most one occurrence and then schedules from the
current time. Missed intervals are coalesced, with original due and observed times
retained; there is no unbounded catch-up replay.

Every scheduled occurrence records its catalog revision, epoch, due/observed/next
times, and outcome. Successful intake includes this record in the same synced
Effect as Task/Run creation. Capacity exhaustion records `skipped`; unavailable
application/workspace/deadline or scheduling admission records `rejected` with a
reason. Both advance the clock in a standalone `service_tick` Effect. Retry of a
committed tick cannot create another Task, including after replay. Manual keys
occupy a separate namespace from scheduled occurrence keys.

Service backpressure is separate from pool capacity:

- `max_instances` defaults to 1 (range 1–32). Dispatch checks service capacity
  before ownership claims, and rechecks it after pool selection. A waiting Task
  exposes `waiting_for: ["service:<id>"]` when service capacity is its blocker.
- `if_running: "skip"` is the default and permits at most `max_instances`
  outstanding Tasks, reserving capacity for receipts waiting on pool/dependencies.
- `if_running: "queue"` permits up to `max_instances + queue_max` outstanding
  Tasks. The queue allowance defaults to 10 (range 1–1000); it uses ordinary Tasks
  and their configured pool ordering. A full queue skips the scheduled occurrence.
- `if_running: "kill"` admits one bounded replacement at capacity, capturing the
  prior Task and waiting for its shutdown, harvest, and claim release. Pending
  event allocation reserves the target; its validated receipt authorizes the stop.
  See [replacement behavior](SERVICE_REPLACEMENT.md) for selection and replay.
- Parked Tasks retain a service slot. Terminal completion releases it. Repeated
  manual keys return their receipt even when full; a new manual key at capacity
  returns a conflict without reserving the key.

The Services page and API expose next occurrence, last occurrence/outcome/reason,
submission/skip/rejection counts, pending/admitted counts, and latest run status.
These values derive from the event log and Task state. The console uses explicit
refresh so editing the catalog is not interrupted by background replacement.
Scheduled execution uses the existing provider admission and limits; configuring
an interval does not raise the recorded unattended grant.

## Calendar schedules

Cron units use five numeric fields and an IANA timezone. Preview includes proposed
clock instants; saved clocks and occurrence records survive replay. See
[cron scheduling](SERVICE_CRON.md) for retained field semantics, daylight-saving
gaps/folds, startup, and downtime coalescing.

## Queue priority

`concurrency.priority` accepts low, normal (default), high, or critical. The saved
adjustment participates in priority-pool selection alongside Task priority and
aging. Existing receipts retain their preference across configuration edits and
retries. See [service queue priority](SERVICE_PRIORITY.md) for scoring, event
allocation, and the relationship to other pool disciplines.

## Service outcome subscriptions

Event units subscribe to final service Task success, failure, or completion with
an event schedule or `triggers` hooks. Matching recipients reserve capacity with
the source outcome, then create downstream Tasks with exact causal evidence.
See [service outcome chaining](SERVICE_EVENTS.md) for matching, retries,
configuration changes, replay, and feedback limits.

## Failure protection

Units can configure a persistent `circuit_breaker` with failure threshold, cooldown,
and bounded recovery probes. It gates manual intake, scheduled occurrences,
event delivery, and queued Task admission. See [service circuit breakers](SERVICE_BREAKERS.md) for
atomic outcome tracking, restart behavior, and policy generations.

## Continuous work

A continuous unit maintains one managed Task, waits for final completion across
its retries, and creates a fresh Task after `restart_delay`. Disabling or replacing
its configuration retires that work before starting a replacement. See
[continuous services](SERVICE_CONTINUOUS.md) for ownership, cancellation, durable
delay, crash recovery, and paused intake.

## Bounded retries and abort

Units also support `restart_policy` with skip/retry/abort, exponential backoff, and
a maximum retry count. Pending retries remain nonterminal Tasks and retain their
original deadline. The Services screen provides cancellation and abort state. See
[service restart policies](SERVICE_RESTARTS.md) for lifecycle and replay semantics.

## Health checks

Command applications can configure unit `health` with file freshness, bounded
command probes, or server-recorded heartbeats. Each execution segment has its own
monitor scope. Unhealthy actions log, kill, or request bounded service retries;
results and stop decisions are durable and stale scopes cannot stop newer work.
See [service health checks](SERVICE_HEALTH.md) for schema, heartbeat CLI/API,
retry/abort precedence, recovery, and the distinction from readiness or acceptance.

## Conditional execution

Units support [conditional execution](SERVICE_CONDITIONS.md) through `condition`:
file existence/absence globs, environment checks, and a bounded command predicate.
All specified conditions must pass before a command or builtin application runs.
The admission-bound policy, check Decision, and result Effect remain inspectable
even when the condition skips the attempt.

## Completion dependencies

`depends_on` requires every named service to succeed in the same scheduler cycle.
It neither activates a prerequisite nor accepts its historical last success.
Manual triggers share a `cycle` key through CLI/API/forms; timed/continuous
cohorts and event siblings receive automatic cycle identities. Admission freezes
exact success witnesses. See [service completion dependencies](SERVICE_DEPENDENCIES.md)
for cycle boundaries, retries, deadlines, and replay.

## Webhooks and external signals

`webhook` services accept JSON at their configured `schedule.path`, keyed by the
sender's delivery ID. `external_signal` services accept a named source and payload
through the signal API or CLI. Both use fixed work bindings, durable payload
provenance, content-checked idempotency, and existing admission gates. See
[service ingress](SERVICE_INGRESS.md) for routes, envelopes, limits, and the local
transport boundary.

## Custom events

`mitos emit-event` and `POST /api/service-events` persist a named JSON event and
its active subscribers in one Effect. Exact names, trailing wildcards, and arrays
use the existing event queue. Source keys are content-checked and survive replay;
initial subscribers share a completion cycle and the root's delivery limits.
The Services page emits events and links receipts to subscriber Runs. See
[custom service events](SERVICE_CUSTOM_EVENTS.md) for payloads and reserved names.

## Stop, restart, and resume

`mitos service-stop --key KEY ID` persists a stop for a unit/target and its recursive
`part_of` members. Admission stops immediately; cancellation, harvest, claim release,
and pending delivery suppression settle before resume is allowed. Independent
roots retain independent holds. `mitos service-resume --key KEY ID` releases one
settled stop and restores ordinary triggers. `mitos service-restart --key KEY ID`
queues a fresh same-cycle group behind the shutdown barrier and releases it together.
Failed preparation leaves the group stopped. The Services page and lifecycle API
share these operations and evidence. See [service lifecycle](SERVICE_LIFECYCLE.md).

## Remaining service contract

Raw worker/scheduler event producers, provider-specific ingress connectors; generalized builtin health
monitoring and readiness gates;
automatic configuration-driven lifecycle propagation; pipeline
service requirements; connectors/sinks; distributed trigger fencing; and hosted
org/project isolation remain unimplemented. The current project scope does not
provide the platform/tenant service boundary in spec `06`. The existing local
provider posture and unmet Studio/Market launch gates are unchanged.
