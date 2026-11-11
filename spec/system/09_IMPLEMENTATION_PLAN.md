# 09 – Implementation and conformance plan

Status: normative migration and acceptance contract, accepted 2026-11-11.
[OS architecture](AGENT_OPERATING_SYSTEM.md) and [durable runtime](DURABLE_RUNTIME.md)
own the model. [Implementation evidence](IMPLEMENTATION_STATUS.md) records shipped
coverage; milestones below are not claims of completion.

## 1. Migration constraints

The implementation MUST preserve current Task/Run/Session IDs, API behavior,
artifact hashes, decision receipts, historical evidence, and interruption holds.
Batch and interactive stores MUST converge under one authoritative lifecycle.
Cutover MUST have one writer for each managed object. Historical Session records
without Task bindings MUST remain explicitly unbound rather than acquire invented
lineage. Data migration MUST be backed up, versioned, validated, and restart-safe.

Source changes MUST keep the local development posture explicit. Requirements
for sandboxing, positive unattended grants, brokering, or billing MUST NOT be
satisfied by configuration labels or prose instructions. Legacy BEAM examples
constrain semantics only; implementation remains Go.

## 2. Delivery order and acceptance

### M1: Common work and execution identity

Introduce explicit Task/Attempt/Process/Session relationships under a shared
admission path. Preserve existing Run IDs as attempts. Bind supervisor assignment,
worker reports, and result evidence to those identities. Expose capability and
implementation gaps through the same API used by all clients.

Acceptance MUST migrate a mixed batch/session fixture, restart, and reproduce
history, artifact integrity, links, pending decisions, and held claims. A late old
worker report MUST NOT complete a new attempt. A worker success report MUST remain
unaccepted until the declared verification path passes.

### M2: Operation receipts and process-host recovery

Persist request identity and payload digests; atomically reserve authority and
resources before dispatch. Add exact launch/inspection receipts and incarnation
checks. A process host MAY be a separate daemon or a provider adapter; the seam
MUST be versioned independently of the UI.

Acceptance MUST kill the control plane between durable admission, process start,
and receipt publication. Retrying the same operation MUST create no duplicate
process. Restart MUST reattach a surviving process by identity, reject reused
PIDs, and retain holds when inspection cannot establish a safe outcome. Client
exit MUST leave execution unaffected.

### M3: Durable supervision, mailboxes, and waiting

Persist supervisory responsibility, coordinator generation, bounded mailboxes,
message/application receipts, wake conditions, and child-cancellation scope.
Assemble replacement context from authoritative state and selected evidence.
Topic discovery remains separate from execution control.

Acceptance MUST replace a coordinator, reject stale proposals/acknowledgments,
recover a crash between mail consumption and dispatch, and deliver a result that
arrives during wait registration. Waiting MUST preserve deadlines and retained
resource accounting. Parent cancellation MUST settle or explicitly detach children.

### M4: Enforced execution and verified continuity

Register at least one provider that actually enforces required confinement,
resource limits, and authority revocation; integrate credential brokering where
required. Carry budget reservations and charges through retries/replacement.
Bind independent acceptance to exact result revisions. Classify pending external
operations and provide reconciliation for unknown outcomes.

Acceptance MUST exhaust a worker budget, replace its harness without resetting
allowances, fail a falsely reported success, and recover a deployment with a lost
response without automatic repetition. A stale process MUST lose access or be
confirmed stopped before conflicting work acquires its claims. Unsupported strict
postures MUST be refused. Restore MUST preserve charges and external-effect records.

### M5: Paired clients and remote execution

Add scoped, revocable client bindings and protocol negotiation. Use the same
objects and receipts from browser/CLI and any additional clients. Then add explicit
remote execution placement with one home authority and host-local enforcement.
Native mobile apps, relays, and push delivery are optional transports.

Acceptance MUST reconnect multiple clients without duplicate operations, revoke a
client, reject unsupported commands, and partition a worker host while retaining
unsafe claims. Already-admitted work MAY continue within locally enforceable
limits; no new authority may be invented. Automatic host failover, replicated
journals, and live migration remain outside this milestone.

## 3. Whole-runtime product gate

The local OS milestone requires M1–M4 and this complete scenario:

1. Create a Task and a bounded supervisory responsibility.
2. Delegate to a real worker, register a wait, and preserve its pending result.
3. Exhaust a bounded allowance and replace the worker with another executor.
4. Restart Mitos and recover pending work, claims, receipts, and decisions.
5. Verify the exact result independently and publish acceptance evidence.
6. Request an explicit human decision for an action outside the recorded grant.

The scenario MUST preserve identity, charges, ownership, and uncertain external
effects without duplicate execution or false completion. Tests MUST exercise
actual local processes and injected crashes, not only model-shaped fixtures.
Model compliance with supplied prompts MUST NOT be treated as kernel enforcement.
M5 adds the distributed deployment gate; it is not implied by a responsive UI.

## 4. Additional application gates

An unattended Studio claim additionally requires the coverage and representative
evaluations in [Autonomy](16_AUTONOMY.md): bounded recovery, independent acceptance,
adequate observation/isolation, and measured human intervention. A recorded zero
unattended grant MUST remain zero until actual admission evidence changes.

A Market launch additionally requires authenticated admission, the required trust
posture, proof cuts, metering, and settlement under [Facilitator](08_FACILITATOR.md).
A local OS release does not satisfy this commercial gate. Conversely, payment
integration is not required for the core OS milestone. The same event and authority
contracts MUST apply when optional commercial workflows are added.

## 5. Responsibility map

| Responsibility | Owning contract |
|---|---|
| Primitive model and authority boundaries | [OS architecture](AGENT_OPERATING_SYSTEM.md) |
| Receipts, transactions, incarnations, mailboxes and federation | [Durable runtime](DURABLE_RUNTIME.md) |
| Execution schemas and agentic policy | [01](01_ARCHITECTURE.md), [04](04_CONTROL_PLANE.md) |
| Providers and launch admission | [05](05_COMPUTE_PLANE.md), [local compute](COMPUTE.md) |
| Scheduling, artifact dependencies and services | [06](06_WORK_POOL.md), [services](SERVICES.md) |
| Verification, recovery and observation | [16](16_AUTONOMY.md), [14](14_OBSERVATION.md), [workspaces](WORKSPACES.md) |
| Knowledge | [07](07_MEMORY.md) |
| Decisions and clients | [15](15_DECISION_PLANE.md), [frontend](../product/11_FRONTEND.md) |
| Packaging and optional commerce | [03](03_CAPABILITY.md), [12](12_APPLICATION.md), [08](08_FACILITATOR.md) |

Each milestone MUST update the implementation ledger with reproducible validation,
remaining gaps, and any compatibility migration. Past test results MUST NOT be
reused as evidence that a newly introduced runtime guarantee works.
