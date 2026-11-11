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

## Atomic admission and persistence

Admission MUST validate assignment/policy revision, caller grant, dependency
witnesses, provider capabilities, budget, and resources. One durable commit MUST
include the transition, attempt identity, reservations, claims, receipt, and dispatch
outbox entry. Either all are visible or none are. No process may start from an
uncommitted proposal; approval alone is not launch success.

The canonical journal is authoritative; projections MUST be rebuildable. A single
writer MUST serialize related mutations, including cross-task admission. Acknowledgment
MUST follow durable commit. Replay MUST validate framing, version, and integrity.
An incomplete trailing write may follow the journal's recovery procedure; corruption
inside committed history MUST fail visibly. An append/fsync failure MUST prevent
further mutation until recovery. JSONL replacement requires a separate ADR.

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
