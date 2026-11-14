# 02 – Execution, authority, and recovery

## Work identity and lifecycle

A Task MUST preserve assignment, inputs, constraints, criteria, parentage, and
accepted progress across attempts. Each launch MUST bind task, attempt, process
incarnation, executor, authority generation, workspace revision, launch receipt,
and session where applicable. Historical unbound sessions MUST remain unbound;
migration MUST NOT fabricate lineage. Native resume that starts execution MUST
record a new execution binding.

| Stage | Required meaning |
|---|---|
| Queued | Work exists; dependencies or capacity may still block admission. |
| Admitted / starting | Authority and resources are reserved; launch outcome may be pending. |
| Running | Exact process incarnation has been established. |
| Waiting | Wake condition is durable; surviving processes and claims remain accounted for. |
| Reported complete | Executor claims an outcome; verification may remain. |
| Accepted | Authorized verification passed for the exact result and criteria revision. |
| Failed / interrupted / cancelled | Execution ended or is unresolved; settlement may remain. |

These are semantic distinctions, not replacement wire enums. Task outcome, attempt
outcome, process exit, and resource release MUST remain separate facts. A failed
attempt awaiting retry MUST NOT terminate its task. A queued attempt receipt MUST
be distinguishable from admitted execution. Retries preserve history and charges.

## Adaptive work graphs

The target composition profile is `work-graph/v1`. It is a versioned composition
of Tasks and Records, not a new execution-attempt namespace. It MUST preserve Run
as an Attempt and MUST NOT replace `ExecutionPlan.runtime` or pretend that existing
batch and interactive adapters have converged. A local application adapter implements a subset of this profile;
[the backend guide](../../docs/adaptive-runtime.md) documents its wire operations and limits.
It does not claim complete profile conformance. [Delivery](06_DELIVERY_AND_CONFORMANCE.md#adaptive-runtime-delivery-gates) owns its gates.

A WorkGraph MUST bind one home server, project, root Task, goal/assignment revision,
structural revision, policy revision, activation origin, and bounded membership.
A Task belongs to at most one graph. A single Task is a valid graph; an agent or
supervisor is not required. Reusable definitions and service templates are separate
from concrete graph instances. External dependencies MUST use explicit references
and accepted input witnesses; importing evidence does not transfer task ownership.
The first profile admits graph members on one home server only.

Each member MUST have a durable ID, specification revision, parent/scope, required
or optional obligation, input bindings, acceptance contract, execution specification,
and policy references. The execution specification identifies an executor selector over registered adapter
revisions, typed invocation inputs or assignment, required provider capabilities,
resources, and limits. Admission resolves the selector and freezes the exact executor.
Agent execution also binds the exact definition, specialization and context receipt
from the [agent library](07_AGENT_LIBRARY.md#binding-and-results). Generated definitions
and the tasks referencing them MUST commit atomically; rejected proposals leave no
selectable orphan definition. This is a negotiated extension, not an operation
implemented by the current local graph adapter.
An agent assignment is executable input, not a substitute for these fields. Free-text roles MUST NOT create permissions or executor restrictions.

Executable work, including planning, decision inference, and verification, MUST have
Task/Attempt accounting. Deterministic reducer operations and durable wait registrations
do not require dummy processes. Executor internals, individual model tokens, and every
tool call need not be graph nodes; independently scheduled, recoverable, or accepted
work SHOULD be. Node kinds MUST NOT require a particular LLM or engineering pipeline.

### Graph amendment transactions

A orchestrator, operator, or deterministic policy MAY propose a bounded amendment.
All proposers MUST use the same validation and receipt contract. A proposal MUST bind
request identity and payload digest, graph ID and expected structural revision,
authority/orchestrator generation, deadline, policy revision, a read set of relevant
fact/specification revisions, and an ordered list of typed operations. Its explanation
and model confidence are evidence, not authorization. Proposal data MUST NOT contain
credentials or choose an unregistered execution destination.

| Operation | Required effect and restrictions |
|---|---|
| Add task | Add a complete specification using proposal-local references resolved to stable IDs in the commit. |
| Amend pending task | Compare specification revision; allow only before admission and before any input/result witness has consumed that revision. Preserve prior specification records. |
| Add/remove dependency | Target pending work only; validate referenced revisions, guards, joins, artifact bindings, and acyclicity against the entire candidate graph. |
| Supersede task | Add replacement work and an explicit lineage/obligation mapping; retain the original task, attempts, evidence, and cancellation/settlement obligations. |
| Set scheduling preference | Change declared priority or eligible executor preference within policy; never bypass hard admission constraints or mutate a running attempt. |
| Open/close expansion | Create or settle a bounded obligation to discover additional work in a named scope. Closing binds the resulting amendment or an explicit no-change outcome. |
| Seal/reopen scope | Freeze/unfreeze membership and required obligations under the closure rules below. |
| Request cancellation | Persist intent and descendant policy; completion of the amendment does not mean processes have stopped. |

Removing a task from a display or cancelling it MUST NOT remove its required
obligation. Removing or weakening an obligation or acceptance criterion requires a
separate, authorized scope/policy amendment with rationale and affected consumers;
ordinary decomposition authority MUST NOT imply permission to lower the goal.
An admitted task MUST NOT be amended in place. Supersession MUST identify whether
old results are excluded, explicitly reused, or require revalidation. Dependents
already admitted retain their old witnesses; changing them requires settlement and
new admission. Supersession alone MUST NOT release claims or stop a process.

Validation MUST resolve temporary references, validate all operation schemas and
registrations, check authority and read-set freshness, reject dependency/containment
cycles, enforce graph/depth/fanout/budget limits, and check affected closure states.
The resulting graph, obligation changes, revision increment, receipt, and wake/outbox
records MUST commit atomically. Invalid proposals publish no partial graph. Structural
edits serialize per graph using compare-and-swap; automatic last-writer-wins merges
are forbidden. A losing proposer MUST reread and submit a new request identity if
content changes. Retrying an identical request returns the original receipt and IDs.

Structural revision changes with topology, specifications, obligations, or closure,
not every log line or task observation. Relevant outcomes, policies, and authority
have separate revisions in the proposal read set. Changes to unrelated logs MUST NOT
invalidate a proposal; changes to consumed evidence MUST. Receipt commit is graph
admission, not process launch. Each subsequent Attempt admission MUST independently
recheck current grants, resources, input witnesses, and task eligibility.

### Attempts, dependencies, and iteration

The common scheduler MUST admit eligible graph Tasks regardless of agent, command,
orchestrator, or verifier executor type. An eligible worker MUST NOT depend on a live
supervisor issuing a launch command. Deterministic selection, capacity reservation,
and the committed dispatch outbox apply equally to interactive and batch adapters.
Unsupported adapter capabilities MUST block with an explicit reason; compatibility
paths MUST NOT advertise graph conformance before their admission contract matches.

An admitted Attempt MUST freeze task/specification and criteria revisions, graph
admission revision, executor and application revision, input witnesses, workspace
baseline, grant generation, provider posture, verification policy, and limits.
A later graph amendment MUST NOT mutate that envelope. Messages MAY guide execution
within it; changing the assignment requires explicit settlement and replacement.
Normal task failure, resource wait, human decision, orchestrator absence, and uncertain
external effect MUST remain distinct observable blocking reasons.

Dependencies use the [scheduling contract](#scheduling-and-dependencies). An edge
MUST distinguish accepted-result availability, declared terminal outcome, and data
materialization; process exit or an agent message cannot satisfy accepted-result
requirements. The graph profile additionally permits a `candidate_available` edge
for a declared verifier: it binds an immutable captured candidate after producing
execution settles, while the producer remains unaccepted. This is not an ordinary
accepted-result dependency. Verifier tasks return verdicts whose admissibility is
checked by the declared acceptance policy; they do not recursively require another
verifier task merely to authenticate and record a verdict. Membership of an
all/any/k-of-n join freezes when its consumer is
admitted, together with exact contributing witnesses. Adding a producer afterward
cannot retrospectively alter the join. Losing branches continue or are cancelled
only according to the recorded cancellation policy, with full settlement accounting.

The admitted dependency graph MUST be acyclic. Iteration is represented by new
Attempts for retries of the same frozen specification, or new Tasks for changed or
additional work, with explicit causal lineage. A correction loop MUST NOT reopen an
accepted historical result or reset counters. A segment/checkpoint recovery within
one Run remains part of that Attempt; it is not a new Task or a hidden budget reset.

Every graph MUST declare finite admitted-work, expansion, orchestrator-call, retry,
depth/fanout, and elapsed-time limits. Reservations MUST account for parallel proposals
and children atomically. Remaining responsibility allowance is shared across tasks,
orchestrator replacement, and attempts; per-attempt limits may narrow it but never
replenish it. Time or allowance exhaustion MUST prevent new work and invoke the
recorded stop/settlement policy. Graph growth without accepted progress MUST have a
bounded stall policy. Token/cost estimates MUST NOT be called enforced spend limits
unless the selected provider supports reservation and metering.

### Expansion, sealing, and completion

A newly adaptive scope is open for expansion. A complete defined scope MAY be
sealed in its creation transaction if the same closure checks pass. Every asynchronous orchestrator/decision
that can introduce work MUST acquire a durable expansion obligation before dispatch;
it records owner generation, triggering facts, deadline, allowance, and expected
outcome. Committing or admitting a graph-relevant event MUST atomically register its
pending trigger before that scope can be considered closed; broker delivery alone
is insufficient. Consuming the trigger MUST atomically create its obligation or
record why no expansion is required. Sealing MUST bind a closure cursor covered by
the orchestrator's processed relevant events, including durably classified no-change
events. Orchestrator silence MUST NOT imply that no more work is needed.

Sealing a scope MUST freeze its membership, required obligations, policy revision,
and acceptance target. It MUST refuse pending expansion obligations, unapplied
accepted proposals, or pending trigger work that can add required members. Sealing
and dispatch of an expansion MUST serialize against the same scope revision. Already
admitted workers MAY continue while a scope is sealed. A new authorized trigger can
reopen a nonterminal scope in a revisioned transaction before admitting expansion;
completion and reopening MUST serialize. A terminal accepted scope is immutable;
new requirements create a new goal revision and graph linked to its predecessor.
A late event for a terminal graph MUST receive an explicit policy outcome: retained
observation, request for review, or authorized new graph. It cannot silently reopen
accepted history. Events not yet received by the home server cannot be treated as
known facts; their later treatment follows this same rule.

Root completion requires all of the following in one validated terminal transition:

- Required scopes are sealed, with no unresolved expansion or relevant decision.
- Required descendant obligations are accepted or discharged by an explicitly authorized scope
  amendment; cancellation, skip, and supersession alone do not count as acceptance.
- The root result passes the declared acceptance policy for exact task, artifact,
  graph, and criteria revisions, as specified in [verification](04_STATE_AND_VERIFICATION.md).
- Child execution, outstanding effects, and claims are settled, or explicitly detached
  under a recorded policy that names their new owner and excludes them from the result.

An empty queue, orchestrator-declared completion, or all currently visible leaves
passing MUST NOT establish goal completion. Sealing is not acceptance. A failed or
cancelled graph retains evidence and settlement status; terminal work outcome and
resource release remain separate. Root acceptance MUST NOT consume an ancestor's
own completion as a dependency: containment/aggregation is distinct from execution
edges, so a parent may await children without creating a dependency cycle.

## Runner runtime and optional decision inference

The runner is a server-owned state machine. An executor performs work; a decision
model supplies a typed observation at selected boundaries; a deterministic reducer
validates and commits transitions. These roles MUST remain separate. A model answer
MUST NOT itself grant authority, invoke a command, resolve a human approval, or
establish acceptance. Models are optional; their absence preserves deterministic
execution and configured recovery.

The implemented batch runtime is `batch-segments/v1`, recorded in `ExecutionPlan.runtime`.
Historical plans without this field use the same segment semantics. Unknown runtime
versions MUST be rejected. The frozen execution plan contains the installed application,
provider posture, initial workspace and oracle fingerprints, absolute deadline,
supervision, and optional decision-model policy. The current implementation freezes
this plan during workspace preparation; catalog freezing at task admission remains
part of the broader admission target. Native interactive execution retains its own
adapter and MUST NOT claim this batch runtime or these model hooks.

One batch Run executes one declared `main` step, with sequential segments representing
execution/recovery phases within the same Attempt. This is a bounded segment machine,
not a general workflow graph interpreter.
Services and Task dependencies compose Runs above it. The phase contract is:

| Phase | Work and permitted successor |
|---|---|
| Prepare | Copy source, preserve baseline and verifier, freeze execution plan; optionally evaluate the initial recovery predicate and checkpoint. |
| Select segment | If configured, evaluate `before_segment`; choose `continue` or `escalate`. |
| Execute | Record segment, invoke installed argv through the provider under the original deadline. |
| Settle | Stop execution, save output, harvest changes, check ownership and oracle integrity. |
| Verify and checkpoint | Independently evaluate eligible results, pin the exact result, record checkpoint; a passing result completes the Run. |
| Select recovery | For an eligible failure only, enforce deterministic restart/stall/deadline policy and choose a passing checkpoint; optionally evaluate `before_recovery` with `rewind` or `escalate`. |
| Restore | Commit recovery intent, restore a new segment workspace, retain abandoned evidence and accounting, return to segment selection. |
| Park or terminate | Preserve evidence and release claims only after settlement; require explicit review when parked. |

Hooks MUST execute with no worker process running. Inference retains the execution
slot and claims until the boundary settles or parks. Only the runtime constructs
allowed choices and binds their operands (segment/input commit or recovery checkpoint).
The model cannot choose commands, checkpoint IDs, budgets, new providers, or arbitrary
successor nodes. Recovery inference MUST run only after deterministic checks; missing
passing checkpoints, exhausted intensity, repeated stalls, human blockers, oracle or
ownership failures MUST NOT be overridden. `before_recovery` requires supervision.
Verification MUST still run after an allowed segment. A successful inference is not
successful work.

### Model decision lifecycle

Each enabled boundary MUST persist a request before sending it. A receipt binds its
protocol version, Run/Task, hook, segment, target, exact projected state, allowed
choices, request ID, journal sequence, deadline, and frozen provider/question policy.
The execution plan supplies the provider configuration without storing credentials.
The journal exposes these as `model_decisions`, distinct from human `decision` cards.

The request is `pending` until a committed result is `accepted`, `escalated`, or
`rejected`. A response MUST match the requested question and choice domain, include
finite bounded confidence and a complete normalized probability distribution, and
select a maximum-probability option. The configured confidence and selected-probability
thresholds are separate checks. Thresholds are application policy, not a guarantee
of calibration or an authority grant. Invalid responses, refusals, low certainty,
missing credentials, provider errors, and inference timeouts MUST stop for review;
there is no implicit provider fallback or permission to continue.

The reducer MUST fence answers against changed run state and expired deadlines.
Committed results precede segment start or recovery intent; those mutations recheck
execution eligibility. Cancellation wins over an in-flight response. Valid late
answers MAY remain evidence but MUST NOT cause effects. Raw response bodies and
transport diagnostics MUST NOT be journaled as errors because they can contain secrets.

Each attempt permits at most its configured `max_calls` (1–64), counting pending and
failed requests, and bounds each call by `timeout_ms` (1–120000) and the original
execution deadline. Rewinds MUST NOT reset this allowance. Reported token usage is
retained when available; unknown remote consumption stays unknown. This is a call
and wall-clock bound, not a token reservation or monetary spending guarantee. The current batch adapter gives an
explicit new Run a fresh allowance, as with existing local retry budgets. Graph
conformance additionally requires the shared responsibility allowance above; this
per-Run limit alone does not satisfy it.

Restart replays receipts without invoking models. A pending receipt MUST NOT be
blindly resent: inference may have happened and been charged. The same boundary
returns its recorded result; an unresolved receipt parks for review. Existing server
restart reconciliation still parks interrupted execution before replacement. No
claim of exactly-once provider execution follows from local request identity.
The current model review card offers a fresh attempt or cancellation; rewind cannot
clear the boundary or reset its allowance. Restoring files cannot rewind model history.

[Interfaces](05_INTERFACES_AND_INTEGRATIONS.md#decision-model-provider-profile) defines
the wire adapters and configuration. [ADR 0003](../ADRs/0003_DECISION_MODELS_AND_RUNNER.md)
records the research and rationale.

## Grants, resources, and providers

A Grant MUST identify issuer, subject, permitted operations, resource scope,
validity, delegation bounds, and budget. Every privileged mutation and admission
MUST validate it. Delegation MUST narrow or preserve limits; replacement MUST NOT
reset authority or spending. Installing a package or receiving a message grants
nothing. A decision authorizes only its declared action and scope.

Resources MUST have stable identity and explicit access modes. Claims MUST bind
owner, task/attempt, generation, scope, and release conditions. Conflicting claims
MUST be queued or refused deterministically. Timeouts, silence, and disconnected
hosts MUST NOT alone release them. Release requires confirmed stop, enforceable
revocation, or a provider guarantee that makes concurrent access safe.

Providers MUST advertise execution, isolation, observation, revocation, and recovery
capabilities. Admission MUST refuse unsupported postures without silent downgrade.
Launch policy MUST be frozen into the attempt. Prompt compliance, a worktree, and
read-only file modes MUST NOT be presented as enforced confinement.

The current local provider runs as the operator. Batch HOME/TMPDIR and credential
hygiene do not restrict host access; interactive agents use native configuration.
Strict unattended operation requires demonstrated authority, observation, acceptance,
and recovery guarantees, not a configuration label.

Credentials MUST NOT enter ordinary task metadata, artifacts, or transcripts.
Where required, a credential broker MUST bind use to an admitted operation and
account for consumption. Revocation MUST fence new requests immediately; surviving
process access must also be stopped or revoked before conflicting reassignment.

Reservations MUST bound admitted consumption. Metered charges and unknown consumption
MUST survive retries and restoration. Concurrency, disk, memory, model spend, and
external-service limits are separate resources. Estimates and prompt instructions
MUST NOT be advertised as hard limits. A waiting VM or shell remains accounted for.

### Resource policy scopes and enforcement

Resource policy separates aggregate **workflow budgets** from **per-agent
execution limits**. One recurring occurrence is one workflow budget. Orchestrator
calls, workers, and retries share that budget; replacements and replay do not
reset it. A later scheduled occurrence receives a new budget from the current
project settings. Existing work keeps its frozen policy.

| Dimension | Scope and unit | Current enforcement |
| --- | --- | --- |
| `workflow.elapsed_minutes` | Wall-clock minutes, including waits and approvals | A finite graph deadline; expiry requests cancellation and waits for process settlement |
| `workflow.attempts` | Attempts allocated across orchestrators, workers, and retries | Durable admission count; allocation consumes an attempt even if it never starts |
| `workflow.tokens` | Aggregate model input and output tokens | Unavailable with native CLI credentials; reported usage is not a reservation |
| `agent.cpu_millicores` | CPU capacity per agent and its descendants; 1000 = one core | Requires an explicit provider quota guarantee; unavailable in the local process provider |
| `agent.memory_mib` | Aggregate memory cap for an agent and its descendants | Requires an explicit provider memory boundary; unavailable in the local process provider |

CPU capacity is a rate, not cumulative CPU time. Per-agent memory is not a host
memory budget or a sum of unqualified RSS samples. A provider must account for
all descendants, including subprocesses that change process groups. A container
label alone does not establish either guarantee. CPU and memory requests are
frozen in the launch posture, checked during compilation, and checked again by
the provider before preparation. Unsupported caps MUST be rejected, including
when another requested resource is supported. Provider selection MUST NOT remove
a requested cap to find an eligible executor.

Each dimension is `{ "mode": "inherit" }`,
`{ "mode": "limit", "amount": POSITIVE_INTEGER }`, or
`{ "mode": "unlimited" }`. Omission means inheritance, field by field, through
built-in defaults, personal defaults, local project overrides, and the project
`mitos` block. An amount on an inherited or unlimited dimension is invalid.
Zero does not encode unlimited. The current graph runtime requires finite
elapsed-time and attempt limits; explicit unlimited is available only for
CPU, memory, and tokens. Built-in defaults are 120 minutes and 96 attempts,
with no CPU, memory, or token cap. Legacy `minutes` and `attempts` retain their
zero-means-inherit behavior; conflicting legacy and scoped values in the same
settings layer are rejected.

The settings API exposes supported controls and units. The UI distinguishes an
uncapped dimension from an unavailable enforcement capability. It MUST NOT
present token telemetry, prompt guidance, or process sampling as a hard cap.
Unsupported structured policy in a directly edited instructions file blocks new
work, just as unsupported policy submitted through the settings API does.

A future hard token budget requires a shared metered gateway: reserve the maximum
admitted request consumption before dispatch, settle actual input/output charges
idempotently, and retain reservations for uncertain requests across retries and
restart. Cache accounting and provider-specific token semantics must be explicit.
Native credentials must not bypass that gateway. This change defines and
validates the token policy dimension; it does not implement that gateway or claim
a spending guarantee. Monetary budgets and total workflow CPU-time budgets are
separate future dimensions, not conversions from elapsed time or token estimates.

## Atomic admission and persistence

Admission MUST validate assignment/policy revision, caller grant, dependency
witnesses, provider capabilities, budget, and resources. One durable commit MUST
include the transition, attempt identity, reservations, claims, receipt, and dispatch
outbox entry. Either all are visible or none are. No process may start from an
uncommitted proposal; approval alone is not launch success.

The configured runtime store is authoritative; projections MUST be rebuildable.
Related mutations, including cross-task admission, MUST be serialized under one
home authority. Acknowledgment MUST follow durable commit. Recovery MUST validate
schema versions and integrity and MUST NOT repeat external effects while rebuilding
state. Storage failure MUST prevent acknowledgment of an uncommitted transition.

The local adapter uses the canonical journal and a single writer. Replay MUST
validate framing, version, and integrity. An incomplete trailing write may follow
the journal's recovery procedure; corruption inside committed history MUST fail
visibly. An append/fsync failure MUST prevent further mutation until recovery.
[ADR 0005](../ADRs/0005_APPLICATION_SERVER_RUNTIME.md) authorizes a planned hosted
PostgreSQL adapter preserving these transition and receipt guarantees through database
transactions. It does not replace local JSONL persistence. Database locking and
constraints MUST prevent concurrent claims or conflicting admissions; storage
adapter selection MUST NOT change Task/Run identity or acceptance semantics.

## Operation receipts and dispatch

Retriable mutations MUST bind authenticated principal, method, stable request key,
and canonical payload digest. Changed content with the same key MUST conflict;
identical requests MUST return the original receipt and outcome. Receipts MUST
distinguish pending, completed, rejected, and unresolved outcomes.

Timeout means a missing response, not proof of failure. Clients MUST query or retry
the original operation rather than create another launch. Retention MUST be bounded
and documented, with tombstone or rejection behavior that prevents expired retries
from executing as new work. A committed outbox drives dispatch; consumers MUST
persist deduplication with state changes. This is not a general exactly-once
guarantee for external systems.

MQTT publication MUST follow a committed outbox entry. A broker PUBACK is transport
confirmation, not proof of durable admission by the destination Mitos server.
The destination MUST commit message identity, payload digest, and inbox state before
issuing an application receipt. Applying it MUST commit the state change, handled
status, and resulting outbox together. Repeated delivery returns existing state;
changed content with the same identity conflicts. The sender retains responsibility
for retry/reconciliation until the destination receipt arrives, even if the MQTT
client acknowledges automatically. Retries preserve identity across lost sessions
and broker restarts. Expiration MUST produce a recorded outcome, not silent loss.

The process host MUST bind launch, inspection, cancellation, and output to operation
identity and exact incarnation. Lost launch responses MUST be resolved against the
original receipt before launching again. Terminal input has a separate bounded
contract in [interfaces](05_INTERFACES_AND_INTEGRATIONS.md#terminal-streaming).

## Scheduling and dependencies

A work pool governs capacity and queue policy. Selection MUST be deterministic for
the same committed state, with declared tie-breaking and starvation behavior.
FIFO, LIFO, priority with aging, and earliest-deadline policies MAY be supported.
Reservations MUST NOT exceed capacity; current local capacity is two workers.

Dependencies MUST identify upstream task, condition, revision, and failure behavior.
Groups declare all, any, or k-of-n semantics. Readiness uses committed task outcomes,
not worker messages. Cycles MUST be rejected before admission. Existing unguarded
edges mean any terminal outcome; CLI `--after` means done, and `--after-failure`
means failed. A pending retry is not a terminal task outcome.

Any/k-of-n witnesses MUST be frozen using terminal event order and deterministic
ties. Later outcomes MUST NOT change admitted inputs. Losing upstream work MUST
NOT be cancelled without declared authority. Dependency amendments MUST be revisioned
and MUST NOT rewrite the evidence of existing admissions.

Artifact inputs MUST bind upstream task, attempt, event, artifact identity, hash,
and size. The local contract allows 1–32 names per edge, at most 128 files and 64 MiB.
Materialization MUST use private verified copies with safe relative paths; missing,
changed, oversized, or escaping inputs block admission. Acceptance MUST recheck
integrity against those same witnesses.

## Waiting, cancellation, and replacement

A wait MUST persist predicate, revision/cursor, deadline, cancellation scope, and
retained resources. Inspecting the predicate and registering the wake condition
MUST be atomic, or use cursor replay with equivalent no-loss behavior. A result
arriving during registration MUST wake the task. Wake application MUST be idempotent
and recheck current authority and eligibility before execution.

Cancellation MUST persist intent and descendant scope before signalling processes.
Queue cancellation prevents admission; dispatched work requires process settlement.
Parents MUST settle children or explicitly detach them under policy. Client closure
is not cancellation. Releasing capacity MUST account for all surviving processes.

Replacement MUST advance the relevant generation and bind a new incarnation. Late
reports MAY remain observations but MUST NOT mutate the replacement. Fencing API
requests does not stop file writes: claims remain until the provider establishes
safe release. Output and partial results MUST remain inspectable within retention.

## Recovery and external effects

Restart MUST rebuild committed state, retain interruption holds, and reconcile
pending operations before conflicting work is admitted.

| Finding | Required action |
|---|---|
| Exact process alive | Reattach only when the provider supports inspection and custody. |
| Process stopped with verifiable result | Harvest, verify, and settle through original identities. |
| Launch definitely not performed | Retry the same admitted operation under current policy. |
| Process or external outcome unknown | Retain holds and reconcile before replacement. |
| Identity mismatch or reused PID | Refuse attachment and preserve the mismatch. |

Recovery MUST distinguish idempotent operations from those that can be queried,
compensated, or only resolved by a person. Missing completion is not proof that a
deployment, publication, or other effect did not occur. Uncertain effects MUST NOT
be blindly repeated, including by a replacement agent using a conversation summary.

Retries MUST have limits, backoff, deadlines, and escalation. Exhaustion MUST leave
a visible blocked/failed state and a concrete decision when judgment is needed.
Restoring files MUST NOT rewind charges, grants, generations, or effect history.
[State and verification](04_STATE_AND_VERIFICATION.md) defines checkpoint integrity;
[orchestration](03_ORCHESTRATION.md) defines coordination above this runtime.
