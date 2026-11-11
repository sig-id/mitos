# Durable runtime contract

Status: normative target, accepted 2026-11-11. This elaborates the
[OS architecture](AGENT_OPERATING_SYSTEM.md) and [ADR 0004](../ADRs/0004_DURABLE_AGENT_OS.md).
Identifiers below are conceptual fields, not newly available API fields.

## 1. Identity and compatibility

Each managed execution MUST bind project, Task, Attempt, Process, execution host,
host incarnation, process incarnation, and authority generation. Supervisor
operations MUST also bind the supervisory responsibility and coordinator generation.
A generation is a monotonically advancing authority token, not a timestamp or PID.

Existing batch Task and Run IDs MUST remain stable. A Run remains an Attempt;
its segments and checkpoint lineage MUST remain inspectable. Interactive Session
IDs MUST remain resolvable through an explicit Session-to-Process mapping.
Migration MUST NOT infer historical Task associations from matching paths, names,
prompts, or timestamps. Unbound legacy sessions MUST be labeled as such.

A Session can display a surviving Process after reconnect. Replacement MUST obtain
a new incarnation and an explicit new Attempt or use the existing recorded
segment-recovery contract. It MUST NOT silently reuse old completion authority.

## 2. Authoritative records and transactions

Accepted intent, lifecycle changes, receipts, grants, reservations, mailbox
settlement, and acceptance MUST be reconstructible from the canonical record.
The current JSONL journal remains the migration baseline. SQL indexes or snapshots
MAY accelerate reads, but MUST be rebuildable at a recorded sequence/hash and MUST
NOT create a second source of truth. Changing canonical storage requires a separate
ADR, migration, and durability tests; adopting Orca's mechanisms does not require
adopting its SQLite storage.

Admission MUST atomically record intent, resource/budget reservations, attempt
identity, and the pending request receipt before dispatch. Settlement MUST
atomically record the effect, receipt, state changes, and downstream readiness or
outbox entries. Cross-task events MUST share one authority-owned transaction;
per-run writers MUST NOT independently commit conflicting cross-task facts.
The initial runtime MAY use one serialized writer.

External execution MUST follow durable admission. An ambiguous write MUST block
mutations until recovery establishes the durable head. Readers MAY return the
last durable state with its revision. Raw output and telemetry MAY use separate
bounded stores; acceptance requires durable evidence references.

## 3. Mutation receipts and ambiguous requests

Retriable control mutations MUST carry a request ID scoped to an authenticated
principal, operation type, and canonical payload digest. The runtime MUST persist
the binding and return pending, completed, rejected, or unknown with the operation
identity and a recovery action.

- Identical principal/request ID, operation, and payload MUST resolve to one
  operation. Concurrent submissions MUST NOT create duplicate effects.
- Reuse with different input MUST fail before any new effect.
- A response timeout MUST NOT authorize a fresh request ID or another launch.
  Clients MUST inspect/reconcile the original operation.
- Receipt reads and retries MUST recheck authorization. Receipts are not grants;
  revocation MUST prevent new privileged actions.
- Retention MUST be declared. Expired request identities MUST be rejected or
  covered by durable tombstones within their validity scope, never silently
  accepted as unseen mutations. Collection MUST preserve replay safety.

Raw terminal input without end-to-end acknowledgments MUST remain non-retriable
following ambiguous delivery. An HTTP receipt proves runtime acceptance, not
consumption by a CLI. Structured steering MUST report these stages separately.

## 4. Process host and lifecycle

The process host MUST describe prepare, start, inspect, attach, interrupt, stop,
and harvest capabilities. A durable launch key MUST bind the exact Attempt,
incarnation, workspace revision, and execution posture. Duplicate starts MUST
return the existing handle or an explicit unknown state. Inspect MUST distinguish
alive, stopped, missing, and unreachable; connection loss MUST NOT imply exit.

The runtime MUST represent preparing, starting, running, waiting, stopping,
exited, and unknown execution conditions independently from Task acceptance.
An exited worker MAY have unverified or partially harvested results. Start/stop
acknowledgments MUST identify the host and process incarnation.

Control-plane restart MAY reattach only after matching the persisted launch
receipt to the process host's identity. PID reuse MUST fail this check. Without
trustworthy inspection, the runtime MUST retain claims and require settlement
before conflicting replacement starts.

Output MUST have bounded retention and stable offsets. Reconnect MUST provide the
requested retained range or an explicit truncation/reset marker. Output, message,
and completion acknowledgments MUST remain separate. Slow clients MUST NOT stall
process output or create unbounded in-memory queues.

## 5. Coordinator and worker authority

A supervisory responsibility MUST have one active coordinator binding in its
authority scope. Replacement MUST atomically advance the generation, bind the
replacement, fence outstanding old-consumer deliveries, and record takeover.
Proposals MUST name their observed revision; stale proposals MUST be rejected or
explicitly re-evaluated.

Worker authority MUST bind Task, Attempt, incarnation, operations, and grant
revision. Completion MUST reject stale, unassigned, or conflicting attempts.
Late observations MAY be retained but MUST NOT settle current work.

Fencing prevents stale runtime mutations; it does not stop an old process from
writing files or calling services. Conflicting replacement MUST wait for verified
stop or provider-enforced revocation of that access. Timeout, lease expiry, and
coordinator change alone MUST NOT release an unsafe claim.

## 6. Control mailboxes and durable waits

Messages MUST carry stable identity, sender authority, destination, causal
operation/attempt references, and payload revision/digest. Deliveries MUST bind
message IDs to consumer generations. Queued, delivered, acknowledged, applied,
and canceled states MUST be distinct where applicable. Duplicate delivery is
permitted; duplicate control-operation application is not. Acknowledgment MUST
be idempotent and reject stale consumers.

Handlers MUST settle consumption and resulting accepted intent in one transaction,
or persist an operation receipt that makes replay converge. Displaying a question
MUST NOT resolve it. Replies MUST bind its revision, intended recipient, and
still-live decision/attempt.

Wait registration MUST atomically record the awaited condition and inspect its
state. An already-present result MUST yield a wake receipt; a later result MUST
enter a durable outbox. Wakes MAY be coalesced, but pending work MUST remain
queryable when notification fails.

Idle supervisors SHOULD release model capacity. Suspension MUST declare retained
processes, memory, claims, and continuing deadlines. A running VM or shell MUST
NOT count as free capacity. Parent cancellation MUST record child scope and
settlement; detached children require explicit authority. Cyclic waits MUST be
rejected or surfaced as durable blocked conditions with a resolution path.

## 7. Grants, budgets, and acceptance

Grants MUST bind principal, resources, operations, delegation ceiling, expiry,
revocation revision, and provider-enforced posture. Child authority MUST be a
subset of parent authority. Admission MUST atomically reserve finite budgets and
capacity. Retry, replacement, and waiting MUST preserve charges and deadlines
unless an authorized amendment is recorded.

Providers MUST advertise actual enforcement. Missing required confinement,
metering, revocation, or resource limits MUST cause refusal. Trusted-local execution
MAY retain a recorded development exception, but MUST NOT inherit strict guarantees
or unattended grants from a name, tier label, or prompt.

Worker success MUST yield a reported-result state. Acceptance MUST bind the Task's
versioned completion condition, exact result/artifact hashes, verifier identity,
output, and actual oracle isolation. Only authorized acceptance may advance
verified dependencies. Criteria amendments MUST be separately authorized and
versioned; workers MUST NOT authorize their own acceptance exceptions.

## 8. Recovery and external effects

Recovery MUST fold the complete committed record, including unresolved intents.
A later unrelated effect MUST NOT hide an earlier incomplete operation. Each
incomplete operation MUST be reconciled independently:

| Operation class | Required recovery |
|---|---|
| Pure computation without external effects | Recompute with recorded inputs and limits. |
| Idempotent provider operation | Query receipt or re-drive the same identity. |
| External system with idempotency support | Reuse its accepted key and reconcile its receipt. |
| External operation with uncertain result | Preserve unknown; inspect external state or request an authorized decision. |

For non-idempotent external effects, intent MUST be durable before crossing the
boundary and completion MUST be recorded afterward. A crash between them MUST
NOT trigger automatic repetition. Missing external deduplication/query support
MUST be disclosed. There is no general exactly-once guarantee across arbitrary
external systems.

Workspace/checkpoint restoration MUST NOT roll back charges, receipts, revoked
grants, or external effects. Reconciliation MUST record observation provenance.
An authorized repeat MUST be a new decision acknowledging possible duplication.

## 9. Clients and remote execution

Browser, CLI, desktop, mobile, and device adapters MUST share operation identities,
authorization, and records, although supported operations MAY differ. Remote
bindings MUST identify runtime and principal, carry scoped permissions, support
revocation, and negotiate capabilities. Reachability or a Session ID MUST NOT
establish authority.

Mutations MUST go to the object's home authority. Remote execution hosts MUST
validate admitted grants/generations and return execution receipts. During a
partition they MAY continue admitted work within locally enforceable limits;
they MUST NOT invent authority. Home runtimes MUST retain claims for unknown
remote executions. Initial federation MUST NOT promise automatic failover,
replicated journals, or live process migration.

Relays MUST transport authenticated protected connections without becoming Task
authorities. Notifications MUST be hints to fetch current state; losing one MUST
NOT lose a question. The handheld v1 API MUST retain its narrow read/resolve/voice
boundary; terminal/remote execution control requires separate protocol capability
and authorization contracts.

## 10. Migration and acceptance

Migration MUST preserve Task/Run/Session IDs, artifact digests, decision receipts,
and URL resolution. Each object MUST have one authoritative writer throughout
cutover. Legacy stores MAY remain read-only evidence. Live dual writes without an
atomic authority transition are prohibited. Unsupported legacy facts MUST remain
unknown, not synthesized as verified history.

Required fault-injection acceptance scenarios:

1. Reconnect two clients without creating a second execution.
2. Lose a launch response; retry the same request and obtain one process receipt.
3. Restart the control plane; reattach by incarnation and reject a reused PID.
4. Replace a coordinator; reject its old mailbox acknowledgments and proposals.
5. Retry a Task; retain a late old-worker report without accepting its result.
6. Receive a result during wait registration; recover a wake without losing work.
7. Crash between mail consumption and dispatch; recover one admitted operation.
8. Exhaust a budget and replace the harness without resetting charges or limits.
9. Lose an external deployment response; reconcile or park without repetition.
10. Partition a remote worker; retain claims until enforced revocation or verified
    stop, and deny replacement when provider guarantees are insufficient.
11. Receive worker success with failed verification; leave the Task unaccepted.
12. Migrate legacy state and restart; preserve links, hashes, history, and holds.

These are required scenarios, not claims of passing tests. The
[implementation plan](09_IMPLEMENTATION_PLAN.md) sequences their delivery.
