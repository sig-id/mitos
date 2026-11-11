# 06 – Delivery and conformance

The specifications define required behavior, including work not yet implemented.
This document owns the delivery order and acceptance gates. Its current-baseline
section is descriptive; it does not establish conformance to the full target.

## Current baseline

As reviewed on 2026-11-11, Mitos is a local Go server with a browser workspace and
HTTP-backed CLI. The implementation has useful but separate execution paths:

| Area | Existing foundation | Material limit |
|---|---|---|
| Interactive work | Native CLI agents, PTYs, projects, supervision links, topics, resume/fork/handover, retained output | Session state and lifecycle remain separate from batch tasks; prompt instructions are not enforcement. |
| Batch work | Durable Tasks and Run attempts, copied workspaces, harvested artifacts, local acceptance, checkpoints, bounded recovery | Same-user provider; verification material is not inaccessible to a hostile worker. |
| Scheduling | Two-worker local capacity, pools, dependencies, artifact inputs, service triggers and lifecycle policies | Does not establish distributed placement or general remote resource custody. |
| Decisions and devices | Persisted local decisions and receipts; optional scoped HTTPS device API | Not a complete paired remote client or multi-tenant identity system. |
| Recovery | Journal replay, retained artifacts and output, interruption holds, explicit inspection before replacement | Server restart interrupts active local execution; general surviving-process reattachment and unified receipts remain target work. |

Batch state uses the `.mitos` journal; interactive metadata uses the desk state
store. Neither their existence nor historical passing tests proves a unified
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
