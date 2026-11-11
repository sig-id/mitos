# 03 – Supervision, services, and decisions

## Durable supervision

The runtime MUST persist a supervisory assignment, coordinator binding, generation,
inbox, child work, decisions, and limits independently of its harness. At most one
coordinator generation may authorize changes for that responsibility.

Coordinators MAY propose decomposition, placement, integration, recovery, and plan
amendments. Proposals MUST identify observed state revision and authority; the
[runtime](02_RUNTIME.md) enforces admission. Already-admitted work MAY continue
while the coordinator is unavailable; work needing new authority waits visibly.

Replacement MUST advance generation, fence stale proposals and mailbox settlement,
and preserve outstanding work. Replacement context MUST include goal, task graph,
accepted artifacts, pending messages/decisions, claims, remaining allowance, and
unresolved effects. Native history supplements these facts without overriding them.

Project analysis MUST produce a reviewable draft before changing saved policy or
starting work. Launch MUST bind the reviewed goal, profile revision, layout, and
limits. Delegation MUST create actual managed work with parentage and assignment.
Background conversations and UI placeholders do not count as workers. Reports bind
exact task, attempt, and incarnation; success still requires verification.

The current workspace allows one active supervisor per project. Agent count and
disk reserve are launch checks; free-text Git and collaboration rules are instructions.
Topics support discovery and discussion through bounded reads, revision-checked
replies, and durable follow cursors. Posts and acknowledgments MUST NOT confer
claims, approve actions, or complete tasks.

## Control messages

Control messages use the [MQTT 5 profile](05_INTERFACES_AND_INTEGRATIONS.md#mqtt-5-messaging).
Logical inboxes belong to durable responsibilities, not MQTT connections or agent
processes. Commands, committed events, and discussion notes share delivery machinery
but retain different authority and completion semantics. MQTT acknowledgment MUST
NOT count as agent acknowledgment, command application, or task acceptance.

Messages MUST have stable identity, sender, destination, causal references, revision,
and relevant task/attempt/generation. Mailboxes MUST bound payload, backlog, retention,
and retries. Backpressure MUST be visible; control work MUST NOT silently disappear.

Delivery, acknowledgment, and application are separate states. Acknowledgment MUST
bind current consumer generation. Applying a command MUST atomically persist its
transition, receipt, and resulting outbox entry. Repeated delivery MUST be safe;
replacement preserves pending messages and fences old consumers. Cancellation and
expiry require receipts. Wait registration follows the runtime's no-loss contract.

## Service definitions and activation

A Service preserves responsibility between executions. Its revisioned definition
MUST contain activation, task template, execution specification, and policies for
duplicates, missed occurrences, overlap, backpressure, retries, and shutdown.
Catalog changes MUST use expected revisions; admitted definitions and inputs freeze.

Explicit selection, targets, presets, `requires`, and `wants` MUST retain activation
reasons. Conflicts are resolved before admission. `requires` enforces its declared
requirement; `wants` is weaker. Neither implies stop propagation.

Manual, interval, cron, outcome, custom-event, webhook, external-signal, and continuous
activation MUST use the common scheduler. Occurrences need durable identity and
receipts. Repeated delivery returns the original result. Queue, skip, and replace
policies MUST be explicit. Low/normal/high/critical priority never bypasses limits.

Replacement with `if_running: "kill"` MUST bind the exact victim, persist stop intent,
and await settlement and harvest before releasing claims. A timed-out stop blocks
replacement. Late events MUST NOT stop the replacement generation.

## Schedules and events

Interval schedules MUST persist cursors and coalesce missed occurrences. Cron uses
five fields, IANA timezones, and conjunction across fields, including day-of-month
and weekday. DST gaps skip nonexistent times; folds produce both UTC instants with
distinct keys. Saved cursors survive restart and unrelated catalog edits. After
downtime, cron admits at most one overdue occurrence before advancing to the next
future match. Schedule changes MUST NOT duplicate existing work.

Outcome events come from final tasks: done produces succeeded, failed produces
failed, and completed matches either. Cancellation produces neither; a failed run
awaiting retry is not final. Multiple matching patterns produce at most one occurrence
per subscription. Newly enabled subscribers MUST NOT replay history implicitly.
Chains MUST retain root identity and enforce depth/fanout bounds.

Custom events bind stable name/key to payload digest. Identical repeats return the
original receipt without rematching subscribers; changed content conflicts. JSON
normalization MUST be specified; current numeric lexical differences remain
significant. Custom names MUST NOT impersonate reserved `service.`, `worker.`,
`task.`, `run.`, `decision.`, or `scheduling_event` namespaces.

Webhook/external-signal ingress MUST authenticate, require an idempotency key, and
accept at most 64 KiB of JSON. Saved configuration selects application, workspace,
and execution policy; payloads cannot grant authority. Receipts distinguish accepted
ingress from completed work. [Interfaces](05_INTERFACES_AND_INTEGRATIONS.md) owns
connector authentication and external delivery.

## Conditions, retries, health, and lifecycle

Conditions MAY inspect declared files, commands, or runtime state under bounded
authority. A miss is a skip, not failure or accepted completion. Service `depends_on`
MUST bind exact successful witnesses for the cycle before acquiring execution claims;
unrelated earlier successes cannot satisfy it.

Retries MUST persist count, backoff, deadline, and retry/skip/abort policy, retaining
accounting and responsibility. Circuit breakers MUST persist closed/open/half-open
state, failure counts, and probe ownership; recovery MUST NOT duplicate probes.
File, shell, and heartbeat checks MUST bind incarnation and probe sequence. Late
results cannot change replacement health. Health is distinct from task acceptance.
Continuous services MUST respect declared concurrency through waits and retries.

Stop MUST create a durable admission hold covering affected tasks, live workers,
and unreleased claims. Propagation follows `part_of`, not `requires` or `wants`.
Stopping MUST NOT silently change configured activation. Resume waits for settlement;
restart crosses a durable stop barrier before one fresh cycle. Repeated requests
MUST NOT duplicate work. Views distinguish configuration, holds, occurrences, and
unresolved resources.

## Human decisions

A decision MUST bind blocked work, causal request, revision, question, attempted
action, constraints, supporting records, and choices with consequences and scope.
It SHOULD be answerable without reading a transcript. A model MAY compose the
explanation; deterministic policy decides authority. Previously granted authority
SHOULD be used within bounds instead of repeatedly requesting it.

Resolution MUST validate caller authority, revision, choice, and value schema.
The server records resolver, channel, time, and receipt. First valid resolution
wins; identical repeats return its receipt and conflicts remain auditable.

Silence MUST NOT create authority. Decisions remain pending unless an explicit,
already-authorized safe default and deadline apply; expiry must differ from human
resolution. Missing facts, credentials, and judgments MUST NOT be invented. Current
local command decisions require explicit resolution.

Waiting retains deadlines, grants, and resource accounting. Checkpointing releases
compute only after the provider establishes safe custody; a parked UI label is
insufficient. Browser, CLI, and device resolutions operate on the same decision.
Notifications, dismissals, and voice transcription are not approval. Reconnect MUST
refresh state before submitting a cached answer; stale or revoked responses fail.
