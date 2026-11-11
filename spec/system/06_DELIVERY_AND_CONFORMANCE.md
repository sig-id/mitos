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
| Decisions and devices | Persisted local decisions and receipts; optional scoped HTTPS device API | Not a complete paired remote client or multi-tenant identity system. |
| Messaging | MQTT 5 via external broker, signed peers and exact grants, journal outbox/inbox receipts, durable notes and atomic service events, local HTTP/CLI | Broker/key configuration is explicit; general control leases and automatic bridges remain outside this adapter. |
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
runtime. The local provider's unattended grant remains zero. Broad remote placement,
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

## Product acceptance

The local operating-system milestone requires M1–M4 and one complete engineering
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
