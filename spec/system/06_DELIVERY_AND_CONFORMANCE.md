# 06 – Delivery and conformance

The specifications define required behavior, including work not yet implemented.
This document owns the delivery order and acceptance gates. Its current-baseline
section is descriptive; it does not establish conformance to the full target.

## Current baseline

As reviewed on 2026-11-11, Mitos is a local Go server with a browser workspace and
HTTP-backed CLI. Interactive metadata and batch state now share the canonical
journal; their execution adapters still have different admission and scheduling paths:

| Area | Existing foundation | Material limit |
|---|---|---|
| Interactive work | Durable assignments, goals, task/attempt/process/session bindings, stable supervision identity and inbox, native PTYs, resume/fork/handover | Reports support explicit human review of frozen text, not independent repository verification. Mailbox application and general coordinator fencing remain incomplete. |
| Batch work | Durable Tasks and Run attempts, copied workspaces, harvested artifacts, local acceptance, checkpoints, bounded recovery | Same-user provider; verification material is not inaccessible to a hostile worker. |
| Scheduling | Two-worker local capacity, pools, dependencies, artifact inputs, service triggers and lifecycle policies | Does not establish distributed placement or general remote resource custody. |
| Agent library | Eight authored default definitions, JSON authoring schema, provenance and native Markdown instructions | Library registration, compatible runtime binding, automatic goal planning and on-demand specialist admission remain design targets; bundled data is not a running planner. |
| Adaptive graphs | Local application adapter: atomic graph amendments, shared Task/Run queue, frozen specs, obligation/closure checks, registered controllers, fenced candidate decisions, native output evidence | PTY admission, cross-graph imports, graph service templates, autonomous root acceptance, and complete G1–G5 conformance remain. See the [backend contract](../../docs/adaptive-runtime.md). |
| Decision inference | Optional System One (Jev/Laya-compatible) and OpenAI Decisions Choice adapters; journaled segment/recovery gates, probability thresholds, bounded calls, cancellation fencing | Batch hooks plus separately journaled graph candidate choices; no predicate/score hooks, model spend enforcement, or automatic replay of unknown inference. |
| Decisions and devices | Persisted local decisions and receipts; optional scoped HTTPS device API | Not a complete paired remote client or multi-tenant identity system. |
| Messaging | MQTT 5 via external broker, signed peers and exact grants, journal outbox/inbox receipts, durable notes and atomic service events, local HTTP/CLI; cron/interval/manual services can also subscribe to events through the same queue | Broker/key configuration is explicit; general control leases and automatic bridges remain outside this adapter. |
| Recurring agent workflows | Goal composer saves instructions with cron and/or external events; each occurrence binds one automatic graph, shares queue limits, preserves trigger context, and tracks graph settlement and results | Workflow algorithms and integration-specific review/merge rules remain agent instructions. Requires the configured native agent runtime. Human requests can hold an occurrence; deadlines stop its graph. |
| Recovery | Journal replay, retained artifacts and output, interruption holds, explicit inspection before replacement | Server restart interrupts active local execution; general surviving-process reattachment and unified receipts remain target work. |

The first startup imports legacy desk metadata into a versioned journal record,
retains its original file as backup, and leaves historical unbound sessions unbound.
New assigned sessions have stable Task IDs and distinct Run/Process/Session IDs;
replacement retains assignment, criteria, report history, and supervisory inbox.
Request keys deduplicate assignment creation and launch admission with payload
checks. Failed persistence restores the published desk state and blocks further
mutations. This is not a process-host launch receipt or a general mutation protocol.

The browser starts at Projects and provides Work, Services, Resources, Inbox, and
Connections views. Project work reads are paginated and terminal output streams only
for the opened session. The local server identity is durable; wrong-server mutation
headers are refused. Independent-server aggregation and pairing are not implemented.

The journal currently stores bounded workspace metadata snapshots. Session/history
limits and the metadata byte limit refuse further writes while preserving existing
records; archival and large-fleet performance remain work. These changes establish
parts of M1 and foundations for M2/M3, not completion of those gates or a fully unified
runtime. In particular, the batch drainer excludes interactive attempts; a shared
Task reader does not establish shared scheduling. The graph application adapter adds automatic dispatch for its registered applications
and decision Tasks, while existing PTYs still use the desk adapter. The local provider's unattended grant remains zero.
Broad remote placement,
hard confinement, general coordinator fencing, and unknown-effect reconciliation
MUST NOT be advertised as complete without the gates below.

## Delivery order

### M1 – Common work and execution identity

Unify Task/Attempt/Process/Session bindings under common admission while preserving
existing IDs and Run semantics. Bind supervision, worker reports, and verification
to those identities. Expose capabilities and remaining gaps through the shared API.

Acceptance MUST migrate mixed batch/session history, restart, and reproduce links,
artifact integrity, decisions, and claims. Historical unbound sessions MUST remain
unbound. A late old worker report MUST NOT complete a new attempt. Reported success
MUST remain unaccepted until its declared verification passes.

### M2 – Receipts and process-host recovery

Add durable request identity, payload digests, atomic reservations, launch/inspection
receipts, and exact incarnation checks. The process-host interface MUST be versioned
independently of clients.

Acceptance MUST inject crashes between admission, process launch, and receipt
publication. Retrying the original operation MUST create no duplicate process.
Recovery MUST reattach a surviving exact incarnation, reject reused PIDs, and retain
holds when inspection cannot establish an outcome. Client exit MUST leave work running.

### M3 – Durable supervision and waiting

Persist coordinator generations, bounded mailboxes, application receipts, wake
conditions, and child-cancellation scope. Build replacement context from runtime
facts and selected supporting records.

Implement the MQTT 5 profile with a conforming broker/client and journal-backed
outbox/inbox receipts. The initial MQTT adapter implements notes and service-event
admission; this alone does not complete the supervision milestone. Acceptance
MUST cover duplicate delivery, lost PUBACK/application receipts, automatic client
acknowledgment before application commit, broker restart, session expiry, deadline
expiry, revoked topic access, and stale coordinator generations. These cases MUST
preserve pending work and prevent duplicate task transitions. Cross-server checks
in M5 MUST preserve these guarantees across independent broker connections.

Acceptance MUST reject stale coordinator proposals and acknowledgments, recover a
crash between message consumption and dispatch, and deliver a result arriving
during wait registration. Waiting MUST preserve resource accounting and deadlines.
Parent cancellation MUST settle or explicitly detach children.

### M4 – Enforced execution and verified continuity

Register a provider that enforces declared confinement, limits, and revocation.
Carry reservations and charges through replacement. Bind independent acceptance
to exact results and classify pending external operations for reconciliation.

Acceptance MUST exhaust a budget, replace an executor without resetting allowance,
reject false success, and recover a deployment with a lost response without blindly
repeating it. A stale process MUST lose access or be confirmed stopped before a
conflicting claim is released. Unsupported strict postures MUST be refused.
Restoration MUST preserve charges and external-effect records.

### M5 – Paired clients and remote placement

Add revocable scoped client bindings, protocol negotiation, and a combined view
across independent servers. Each project retains one home authority. Then add
explicit remote worker placement with host-local enforcement. Native clients,
relays, and push delivery are optional transports.

Acceptance MUST reconnect multiple clients without duplicate mutations, revoke a
client, reject unsupported commands, and partition a worker host while retaining
unsafe claims. Already-admitted work MAY continue within locally enforced limits;
no new authority may be invented. Automatic failover and live migration are excluded.

The multi-server check MUST connect one client to a desktop server owning Project A
and a laptop server owning Project B. With overlapping local names or IDs, actions
MUST reach only the intended server. Disconnecting B MUST leave A usable and mark
B's cached state stale. Reconnection MUST reconcile B's pending receipts without
duplicate work; revoking access to one server MUST NOT revoke or expand the other.

## Adaptive runtime delivery gates

`work-graph/v1` is a target profile. These gates refine M1–M4; they do not bypass
receipts, custody, or enforcement requirements. Local trusted execution MAY implement
the graph contracts while explicitly declining strict isolation/spend capabilities.
A graph UI or successful model call alone establishes none of these gates. Implement
G1 and G2 before adaptive mutation, then G3 before general controller activation.

### G1 – Common admission and automatic dispatch

Route agent, command, planner, decision, and verifier execution through common
Task/Attempt admission, capacity, receipts, and resource custody. Preserve existing
Run IDs, batch segment semantics, and native session access. Acceptance MUST admit a
mixed agent/program dependency chain without a supervisor issuing launches, exhaust
shared capacity without oversubscription, replace an executor while retaining lineage,
and refuse unsupported adapter postures. Inject a crash after admission and before
launch acknowledgment; original receipt reconciliation must prevent duplicate work.

### G2 – Atomic graph amendments and immutable attempts

Implement the profile schema, canonical digests, read sets, structural compare-and-swap,
obligation records, finite limits, and atomic wake/outbox publication. Acceptance MUST
cover valid multi-task fanout, duplicate identical requests, changed payloads under
the same request key,
concurrent proposals against one revision, malformed local references, whole-graph
cycles, oversized growth, and storage failure at the commit boundary. No rejected
proposal may publish nodes or consume execution slots. Admitted task inputs and
criteria MUST remain unchanged after subsequent amendments. Unrelated log writes
MUST NOT invalidate a proposal; changed consumed evidence MUST fence it.

### G3 – Evidence, joins, iteration, and closure

Implement exact artifact inputs, frozen joins, expansion obligations, seal/reopen,
root acceptance, cancellation, and supersession with settlement. Acceptance MUST
race fanout against sealing, root completion against a new trigger, and any/k-of-n
admission against late producers. Reject empty-queue completion with an unresolved
planner or unconsumed relevant event; reject dropping a failed required check as a
completion shortcut. Crash after event admission but before controller delivery must
leave the closure blocker visible. A repair
must produce a new candidate and cannot reuse a verdict over different content.
Verify a candidate before producer acceptance without a dependency deadlock, and
record its declared verdict without recursively spawning verification of verification.
Superseded running work MUST retain custody until settled. Test bounded correction
loops and prove that attempts/replacement do not reset responsibility allowances.

### G4 – Adaptive controllers and native-agent observations

Implement deterministic and agent planners, optional typed decisions, the
[agent library](07_AGENT_LIBRARY.md), frozen specialization/context, event cursors,
generation fencing, and native-output adapters. Cover library revisions, scoped
discovery, generated specialists and compatible runtime resolution.
Acceptance MUST generate previously unspecified tasks from new evidence and run them
through G1–G3. Use an agent fixture with ordinary prose and native events, with no
PASS/FAIL/FIX tags or graph API syntax; preserve the source and show the interpreted
claim/proposal separately from its admitted effect. Ambiguous or forged command-like
text MUST NOT mutate work. Freeze template specialization and generated invocation
content per attempt; changes to a template MUST NOT alter an admitted worker.

Inject crashes before model dispatch, after a response, after answer commit, and
before proposal commit. Replay MUST perform no inference; unresolved inference remains
visible; a committed answer may apply only its original candidate after revalidation.
Cover low confidence, refusal, invalid operands, revoked authority, model timeout,
duplicate trigger delivery, controller replacement, and cancellation during inference.
A retired controller cannot mutate the graph. A waiting root must not exhaust worker
capacity solely by existing. Existing eligible work MUST progress
while the controller is offline. Meaningful event coalescing MUST not lose triggers
or repeatedly infer on unchanged state. Protocol fixtures prove runtime handling;
labeled scenario evaluations separately establish decision/planner quality.

### G5 – Adaptive service instances and operator journey

Apply manual, schedule, and event activation to the same graph template and controller
policies. Duplicate occurrences MUST return the same root; subsequent occurrences
MUST get distinct graphs and cannot consume unrelated previous-cycle results.
Graph-wide skip/queue/replace policy MUST be tested independently of per-task limits.
Stop/restart must settle the whole declared scope before admitting its replacement.

The end-to-end journey MUST start with a goal without manual step construction or
example executor selection. Use the default library, generate a specialist when
no exact role fits, and distinguish missing runtime/authentication from busy capacity.
Without a prewritten task pipeline it MUST discover parallel work, adapt an agent
template, choose domain/Git operations from
evidence, insert a previously unspecified recovery task, replace a controller,
restart the server, and accept an exact result. Repeat a defined-plan case without
model calls and a hybrid case whose mandatory check the planner cannot remove.
Expose current graph, causal amendments, waiting reasons, evidence, and concrete
human decisions through the same read model. Test bounded reads and backpressure
with declared fleet/graph sizes; synthetic large graphs do not establish equivalent
concurrent-process capacity.

## Application-server delivery gates

These gates extend the local milestones; they do not assert hosted readiness. The
current build has no PostgreSQL runtime adapter, general remote worker protocol, or
complete multi-tenant application API. Acegoat is a design consumer in a separate
repository. The initial slice MAY proceed alongside local delivery; it MUST NOT
claim unrelated M1–M5 or G1–G5 guarantees. One home authority with remote workers does
not require multi-server aggregation, but does require the relevant M2–M4 custody,
receipt and enforcement guarantees and M5 remote-placement checks.

### S1 – Headless structured work

Admit a structured-input task without a Git repository, workspace or PTY, execute it
through common Task/Run admission and accept an exact typed result under declared
policy. Verify independent application/API version negotiation and unsupported-version
refusal. Keep the existing local startup and engineering workflow usable without a
database service, broker or remote worker. Publish actual adapter capabilities.

### S2 – Hosted persistence and application boundary

Implement the PostgreSQL runtime adapter with the same atomic transition, receipt,
outbox and recovery guarantees as local persistence. Define schema migration,
backup/restore and receipt retention behavior before deployment. Test crash recovery
at commit boundaries and refusal of changed payloads under a reused request ID.

A separate application fixture MUST save a message and dispatch intent atomically,
admit a Mitos Task, consume its accepted result, and publish one durable reply. Inject
crashes before/after admission and before/after application publication; replay MUST
retain one logical task and one published reply. No cross-service transaction or
direct runtime-table access may be assumed. Verify authenticated tenant isolation for
reads, writes, context, artifacts, notifications and receipts, plus capacity fairness
and enforcement of configured tenant limits.

### S3 – Replaceable remote workers

Run S2 through a separately deployed worker using the versioned claim/context/
checkpoint/result contract. Inject worker crash, expired lease, duplicate results,
lost acceptance receipt, server restart and missed MQTT notification. Recovery MUST
find pending work, reconcile committed results, and reject a late replaced worker's
mutation. Demonstrate enforceable revocation or settlement before conflicting work
starts; a timer alone is insufficient. An external-action fixture MUST lose its
response and reconcile the original operation without blindly repeating it.

### S4 – Dormant responsibilities and conversation consistency

Demonstrate a personal-agent responsibility coordinating independent background
negotiations. A negotiation MUST checkpoint and release capacity while waiting for
an answer. Race the answer with wait registration, restart, and coordinator replacement;
there MUST be no lost wake-up or duplicate application. Serialize personal-conversation
updates and reject or reconsider results based on superseded preferences. Verify that
candidate and company context stays within its authorized thread and that only
permitted outcomes reach the personal conversation.

Measure bounded storage/read behavior and scheduler progress at declared numbers of
tenants, dormant responsibilities and active workers. Large numbers of dormant
records MUST NOT be advertised as equivalent concurrent execution capacity. These
fixtures establish reusable runtime behavior; Acegoat owns its domain acceptance tests
and integration release independently.

## Product acceptance

The local operating-system milestone requires M1–M4, G1–G5, and one complete engineering
journey: create a bounded responsibility, delegate real work, wait for its result,
replace an exhausted worker, restart the server, verify the exact result, and
resolve a concrete human decision outside the original grant.

The journey MUST preserve assignment, ownership, charges, pending decisions, and
uncertain external effects. Tests MUST use real local processes and injected
failures, not only model-shaped fixtures. M5 adds the distributed access and execution
gate; a responsive mobile view does not establish it.

Conformance reports MUST identify implementation revision, reproducible checks,
satisfied guarantees, remaining gaps, and compatibility changes. Results belong
with their change or release; this specification MUST NOT become a chronological
test ledger. Historical sample JSON is not a normative requirement.

## Change discipline

Implementations MUST preserve existing compatible behavior until an explicit
migration is accepted. Architectural changes MUST update their owning specification
and ADR. Product claims MUST follow demonstrated capabilities. Product and
system documents MUST remain independently numbered consecutively, with one owner for each
contract rather than overlapping documents that silently disagree.
## Sessions, routing and supervision delivery

The six slices in [agent sessions and supervision](08_AGENT_SESSIONS_AND_SUPERVISION.md)
are required targets. At design adoption, bridge CLI requests and typed batch/graph
choices exist, but UI bridge launch, project delegated approval settings,
COOPERATION.md snapshots, request classification, live automatic-worker requests,
and independent model supervision are not yet integrated. Each implementation PR
must report its validated capabilities and remaining substrate limitations.
