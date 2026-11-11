# 08 – Services and events

## Definition and activation

A Service preserves an ongoing responsibility between executions. Its definition
MUST include activation, task template, execution specification, operating policy,
and revision. Policy MUST cover duplicate delivery, missed occurrences, overlap,
backpressure, retries, and shutdown. A service with no running process still exists.

Catalog changes MUST use expected revisions. Activation through explicit selection,
targets, presets, `requires`, or `wants` MUST retain its reasons. Conflicts MUST be
resolved before admission. A `requires` relationship MUST enforce its declared
requirement; `wants` is weaker. Neither implies lifecycle stop propagation.

Manual, interval, cron, task-outcome, custom-event, webhook, external-signal, and
continuous activations MUST use the same scheduler and authority checks.
Definitions and bound inputs MUST be frozen for each admitted occurrence.

## Occurrences and overlap

Each occurrence MUST have durable identity and a receipt. Repeating its delivery
MUST return the original result rather than create another task. Overlap policy
MUST explicitly queue, skip, or replace existing work. Queue priorities are low,
normal, high, and critical; priority MUST NOT bypass resource limits.

Replacement with `if_running: "kill"` MUST identify the exact victim, persist stop
intent, and wait for process settlement and result harvest before releasing its
claims. A timed-out stop MUST leave replacement blocked. Older delayed events
MUST NOT stop the replacement generation.

## Schedules

Interval scheduling MUST persist its cursor and coalesce missed occurrences under
its declared policy. Restart MUST NOT replay an unbounded backlog.

Cron uses five fields and an IANA timezone. The contract MUST preserve the current
field conjunction semantics, including day-of-month with day-of-week. Nonexistent
local times during a DST gap are skipped; repeated wall-clock matches during a
fold produce both UTC instants with distinct occurrence keys. Saved cursors MUST survive restart and unrelated catalog edits. After downtime,
at most one overdue occurrence is admitted before advancing to the next future
match. Schedule changes MUST NOT duplicate already admitted work.

## Task outcomes and custom events

Service outcome events MUST come from final task outcomes. `done` produces
`succeeded`, `failed` produces `failed`, and `completed` matches either. Cancellation
produces neither. A failed run awaiting retry MUST NOT emit a final failure event.
Multiple matching patterns MUST produce at most one occurrence per subscription.
Enabling a subscriber MUST NOT replay historical events unless explicitly requested.

Event chains MUST retain root identity and enforce depth and fanout bounds.
Custom events MUST bind a stable name/key to a payload digest. Repeating identical
content returns the original receipt without rematching subscribers; different
content with the same identity MUST conflict. JSON normalization MUST be specified;
current numeric lexical differences remain significant.

Custom names MUST NOT impersonate reserved runtime events, including `service.`,
`worker.`, `task.`, `run.`, `decision.`, and `scheduling_event` namespaces.
Payloads are input data, not authority to change execution policy.

## Ingress

Webhook and external-signal ingress MUST authenticate callers, validate a stable
idempotency key, and accept at most 64 KiB of JSON. The same key and content MUST
replay its receipt; changed content MUST conflict. The application, workspace,
and execution policy MUST be selected by saved configuration, never arbitrary
incoming payload fields. A receipt MUST distinguish accepted ingress from completed
work. See [applications and integrations](13_APPLICATIONS_AND_INTEGRATIONS.md).

## Conditions and completion dependencies

A condition MAY inspect declared files, commands, or runtime state before work.
A condition miss MUST record a skip, not a task failure or accepted completion.
Checks MUST be bounded and MUST NOT silently acquire broader authority.

A service cycle with `depends_on` MUST bind the exact successful dependency
witnesses before acquiring execution claims. Previous unrelated successes MUST NOT
satisfy a new cycle. Dependency failure, absence, and uncertainty MUST remain visible.

## Retries, breakers, and health

Retry policy MUST persist attempt count, backoff, next deadline, and exhaustion
behavior. Retry, skip, and abort decisions MUST survive restart. Retries MUST keep
the same responsibility and accumulated accounting; they MUST NOT emit terminal
outcomes before the task settles.

Circuit breakers MUST persist closed/open/half-open state, failure counts, and
probe ownership. Recovery MUST not admit multiple half-open probes by accident.
Health checks MAY use file, shell, or heartbeat observations; every result MUST
bind the monitored process incarnation and probe sequence. Late probe results MUST
NOT change a replacement's health. Unhealthy execution and accepted task failure
are different facts.

Continuous services MUST maintain at most the declared concurrent responsibility,
including waits and pending retries. A heartbeat without matching identity MUST
NOT prove that the intended worker remains healthy.

## Lifecycle

Stop MUST create a durable admission hold and capture the affected tasks, live
workers, and unreleased claims. Propagation follows declared `part_of` scope;
`requires` and `wants` alone MUST NOT propagate stop. Stopping a service MUST NOT
silently edit its configured activation policy.

Resume MUST wait until the stop scope settles. Restart MUST cross a durable stop
barrier before creating one fresh cycle. Repeating either request MUST not duplicate
work. State views MUST explain configured activation, effective holds, active
occurrences, and unresolved resources separately.
