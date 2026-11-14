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

## Adaptive controllers

A controller owns a declared planning scope within a supervisory responsibility.
Its policy MUST bind eligible event types, deterministic relevance filters, permitted
proposal operations, mandatory obligations, executor/capability allowlists, limits,
and conditions requiring human judgment. Policy and controller generation are
revisioned independently of agent conversation history. Authority to decompose work
MUST NOT imply authority to remove a mandatory check, publish externally, or enlarge
allowances. Multiple planners MAY produce candidates; only the current authorized
controller generation may submit a controller mutation for its scope. Operator
mutations still serialize through the same runtime transactions.

| Planning policy | Source of concrete work | Runtime invariant |
|---|---|---|
| Defined | Operator or installed deterministic definition supplies tasks and transitions. | Scheduler advances eligible work without a supervisor launch loop. |
| Adaptive | A planner derives tasks and amendments from the goal and committed evidence. | Every accepted amendment passes the same admission checks. |
| Hybrid | Fixed obligations constrain adaptive decomposition and repair. | Adaptive work cannot bypass mandatory obligations or acceptance policy. |

These policies MUST compose with manual, scheduled, and event activation and with
agent or program executors. They MUST NOT be separate task stores or incompatible
work types. A decision provider is optional in every policy. Deterministic facts
such as dependency readiness, queue capacity, deadlines, and authenticated authority
MUST be evaluated by code rather than repeatedly delegated to a model.

### Planning and typed decisions

Planning generates fully specified candidate amendments. It MAY use an agent,
installed templates, deterministic expansion, or an operator. A planner's output
is a proposal, not executable authority. Installing or generating new executable
capabilities requires its own authorized application operation; a graph amendment
references registered adapters and capability revisions. Registration MUST NOT require
pre-enumerating each task, generated script, tool argument, Git command sequence, or
repair path. An authorized command/tool adapter MAY execute dynamically produced
instructions bound to their exact content and existing scope. Generated content
cannot install an adapter, expand host access, or grant itself new authority.

Decision evaluation judges a bounded supplied set of alternatives or evidence.
Choice may select a concrete candidate; Predicate/Noul or Score may inform a declared
policy when their provider mappings are implemented. These answer types MUST NOT
be treated as arbitrary task/argument generation. To discover unknown work, the
controller may select an authorized planning task, then evaluate its proposals.
Candidate generation and decision evaluation MUST have separately recorded attempts,
inputs, limits, and outcomes. No pipeline of models is required for a deterministic
or operator-supplied amendment.

Before inference, the runtime MUST validate candidate schemas and exclude ineligible
operations using current facts, grants, and remaining limits. The request MUST bind
the exact candidate set/digest, concrete operands, projected facts/read set, graph
revision, controller generation, policy, provider/model configuration, deadline,
and allowance reservation. An answer selects only a supplied candidate; it cannot
substitute task IDs, commands, criteria, budgets, or other operands. Commit MUST
revalidate all affected facts after inference because prevalidation is not a lock.

Persist request intent before sending and validated response before graph commit.
Decision evaluation and proposal application are separate receipts: an accepted
model answer is not an applied amendment. A crash between them MUST recover the
same candidate and recheck eligibility without reinvoking the model. A changed fact,
revoked grant, cancellation, expired deadline, or obsolete generation fences the
mutation. Late answers MAY remain evidence. Replay MUST NOT issue inference or
external effects. Unknown inference MUST retain its reservation/usage uncertainty
and require reconciliation or an explicitly authorized fresh evaluation; blindly
resending the original request is forbidden.

Policy MUST define acceptable response types, thresholds, refusals, uncertainty,
timeouts, candidate rejection, and bounded replanning. It MUST include a visible
wait/escalation outcome when no candidate is eligible or justified. A malformed or
missing answer MUST NOT mean continue. Numerical confidence MUST NOT count as a
correctness guarantee, permission, or result acceptance. Provider fallback requires
explicit policy, a new receipt, and accounting; it cannot silently change data scope.
The existing batch hook profile remains narrower and retains its own contract in
[runtime](02_RUNTIME.md#model-decision-lifecycle).

### Agent templates, context, and domain adaptation

The [agent-library contract](07_AGENT_LIBRARY.md) owns reusable definitions,
specialization, knowledge/context and binding semantics. Controllers MUST select
suitable library definitions or create graph-scoped specialists within policy;
they MUST NOT require users to assemble ordinary worker steps. A suitable role can
have many fresh instances; no idle instance is required before admitting new work.
Creation does not install a runtime, enlarge capacity or confer additional authority.

Definition revision, specialization and resolved runtime/context MUST be frozen in
the Attempt. Changes affecting an admitted contract require replacement; additional
context or guidance within that contract follows ordinary observation/steering rules.
Template changes cannot silently alter existing attempts or mandatory acceptance.

Agents SHOULD work with their task context, knowledge, data, and native tools. They
MUST NOT be required to emit framework-specific PASS/FAIL/FIX tags, transition labels,
JSON envelopes, or graph-edit syntax in ordinary replies. Adapter observations and
controller interpretation provide the runtime boundary defined in
[interfaces](05_INTERFACES_AND_INTEGRATIONS.md#native-agent-observation-contract).
An agent MAY opt into structured tools, but ordinary text remains a supported source
of reports and planning evidence. An agent's explanation of success remains a claim,
regardless of its formatting.

Domain workflows MUST be constructed from authorized capabilities and current evidence.
For example, a controller may choose a worktree, request a commit, compare changes,
introduce integration/review tasks, or revise a merge strategy only when needed.
No fixed Git lifecycle or mandatory software-engineering role sequence is implied.
Workspace ownership, conflicting writes, acceptance, and uncertain external effects
still obey kernel contracts. Domain exceptions can prompt newly generated work;
process failures, stale authority, unknown effects, and invalid graph edits retain
explicit deterministic handling even when a model proposes their recovery.

### Event-driven coordination and replacement

The runtime MUST persist a controller cursor, trigger identity, pending expansion
obligations, candidate/decision receipts, durable waits, and remaining allowance.
Relevant task outcomes, accepted artifacts, failures, decisions, and service inputs
can wake it. Event consumption and expansion/wait registration MUST follow the
no-loss receipt contract. Duplicate delivery cannot create duplicate tasks or calls.
Coalescing MUST retain contributing event identities and a bounded durable window;
it MUST NOT discard unresolved work or cause starvation under continuous updates.

Controllers SHOULD receive bounded projections and artifact references selected for
their scope. Reads MUST disclose truncation and unavailable evidence; planners cannot
act as though omitted facts were observed. Model calls SHOULD occur on meaningful
state changes, not scheduler polling ticks or terminal byte updates. Independent
eligible work continues during inference. Controller compute, model calls, workers,
and retained claims MUST be accounted separately, including waits and cancellations.
A logical root or persisted wait MUST NOT occupy a worker slot merely by existing.
A live waiting process still consumes its actual resources; checkpointing/yielding
releases compute only when provider custody permits it. Controller capacity and
worker capacity MUST have explicit admission policy so passive coordination cannot
silently consume all capacity required by its children.

If a controller disappears, admitted work continues within its frozen limits and the
scheduler may admit existing eligible tasks under valid standing policy. New planning
waits or activates a replacement under declared policy. Replacement advances generation,
fences old proposals, and reconstructs the graph, obligations, pending decisions,
evidence, effects, and remaining allowance. It MUST NOT regenerate already committed
children from a narrative summary. Controller exhaustion produces a visible planning
block; it MUST NOT silently accept the goal or require cancelling unrelated workers.

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

Under the adaptive graph profile, a service occurrence MUST create one root Task and
one graph instance bound to the occurrence receipt and frozen template/policy revision.
Duplicate delivery returns that instance; a later occurrence creates new identities.
One occurrence's outputs MUST NOT satisfy another's dependencies without an explicit
cross-instance evidence binding. Manual and scheduled creation use the same admission
path. Graph-wide overlap/replace policy applies to the entire occurrence, separately
from concurrency of individual member tasks. Stop/restart MUST cross the settlement
barrier for the declared descendant scope before creating a replacement occurrence.
A service remains active between completed graphs; it is not itself a completed Task.

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

A manual, interval, or cron service MAY declare additional `events` patterns.
Clock occurrences and matched events MUST target the same durable service queue,
sharing activation, stop holds, maximum instances, and pending capacity. Event
delivery MUST retain its source receipt and MUST NOT advance the clock cursor.
Local clocks MUST work without a broker; MQTT is the external transport, not the
authority for local scheduling. Both paths commit admission to the journal.
Catalog edits to event subscriptions MUST suppress pending deliveries whose frozen
subscription no longer matches, without replaying earlier unmatched events.

### Recurring agent workflows

The goal composer MUST allow instructions to be saved for a cron schedule, an
external custom event, or both. Workflow-specific algorithms and integration rules
belong in those instructions and agent-authored project resources, not in Mitos
domain adapters. For example, how to discover PRs, interpret a security review,
and decide whether to merge is part of the user's workflow. Mitos supplies triggers,
durable admission, agent execution, limits, lifecycle, and evidence.

The local `agent-workflow` service binding MUST save a project, instructions, and
per-occurrence time limit. Each admitted occurrence MUST create at most one automatic
work graph, bound durably to the occurrence run. Creation retries and restart after
admission MUST recover that same graph; they MUST NOT execute a second copy. A later
occurrence gets a fresh graph and reads current state according to the instructions.
The work binding freezes when the occurrence is queued; project/agent execution
settings freeze when its graph is created, as for manually started automatic work.

Trigger payload and provenance MUST be passed to the planner and workers as bounded,
untrusted context, separately from the user's instructions. Event data cannot change
the workflow's project, instructions, grants, or limits. External changes require
authorization in the saved user instructions under the existing agent policy.
Waiting on a domain condition SHOULD normally finish the occurrence with a report
so a later trigger can check again, rather than occupy an agent indefinitely.

A logical occurrence MUST hold its service concurrency slot until its graph settles,
without holding a process slot that its child agents need. Workers use the ordinary
graph queue, pool admission, claims, and budgets. Stop, replacement, and timeout MUST
cancel the graph and await child process/claim settlement before closing the
occurrence. A restart MUST preserve pending approvals and interrupted child evidence;
it MUST NOT silently retry an interrupted agent's external effects.

The console MUST show saved instructions, trigger configuration, queue state, and
the latest graph/result link. Successful graph completion MUST save its result on
the occurrence, without labelling the agent's report as independent verification.
Start failures and expired occurrences MUST remain visible and release capacity
after settlement. Completed graphs MUST NOT exhaust the active graph allowance.

### Outcome and custom events

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

Coordinator planning, request routing, and independent boundary supervision are
separate responsibilities. Their vocabulary and delegated automated-resolution
contract are defined in [agent sessions and supervision](08_AGENT_SESSIONS_AND_SUPERVISION.md).
Human-only requests retain the rules below; delegated automation requires the
explicit scope and revalidation defined by that contract.

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
