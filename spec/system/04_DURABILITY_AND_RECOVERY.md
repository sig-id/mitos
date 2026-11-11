# 04 – Durability and recovery

## Authoritative record

The canonical journal is authoritative; indexes and UI projections MUST be
rebuildable. A single writer MUST serialize related mutations. Cross-task admission,
claims, budget reservations, receipts, and outbox entries MUST have one atomic
commit boundary. An acknowledgment MUST NOT precede durable commit.

Replay MUST validate record framing, version, and integrity. An incomplete trailing
write MAY be recovered by the documented journal procedure; corruption inside
committed history MUST fail visibly. Recovery MUST NOT synthesize success, erase
charges, or replay model reasoning. Replacing JSONL storage requires a separate ADR.

## Retriable operations

Every retriable mutation MUST bind an authenticated principal, method, stable
request key, and canonical payload digest. Reuse with different content MUST
conflict. Repeating the same request MUST return its existing receipt and outcome.

Receipts MUST distinguish pending, completed, rejected, and unresolved outcomes.
A timeout means the caller lacks a response; it does not mean the operation failed.
The caller MUST query or retry the same identity rather than issue a new launch.
Retention MUST be bounded and documented. Expired receipts MUST leave sufficient
tombstone or rejection behavior to prevent a delayed retry executing as new work.

A committed outbox entry MUST drive dispatch. Consumers MUST persist deduplication
and state changes together. This prevents duplicate internal transitions; it does
not establish exactly-once behavior for arbitrary external systems.

## Reconciliation

At restart the server MUST rebuild state, retain interruption holds, and inspect
pending operations before admitting conflicting work.

| Finding | Required action |
|---|---|
| Exact process is alive | Reattach if the provider supports custody and inspection. |
| Process stopped with verifiable result | Harvest, verify, and settle through the original identities. |
| Launch was definitely not performed | Retry only the same admitted operation under current policy. |
| Process or external outcome is unknown | Retain holds, expose uncertainty, and reconcile before replacement. |
| Provider identity mismatches or PID was reused | Refuse attachment and preserve the mismatch as a recovery fact. |

Recovery MUST distinguish an idempotent operation from one that can be safely
queried, compensated, or only resolved by a person. An absent completion record is
not evidence that an external action did not occur. Deployments, publications,
payments, and similar effects require operation-specific reconciliation.

## Checkpoints and restoration

A checkpoint MUST bind a task revision, workspace/artifact digests, accepted results,
and the runtime records required to explain it. Restoring files MUST create a new
state transition; it MUST NOT rewind accounting, decision receipts, generations,
or external-effect history. Restored work MUST be revalidated before acceptance.
See [verification and checkpoints](10_VERIFICATION_AND_CHECKPOINTS.md).

## Recovery bounds

Retries MUST have explicit limits, backoff, deadlines, and escalation policy.
Recovery MUST preserve remaining allowance and resource ownership across attempts.
Exhausting recovery policy MUST leave a visible blocked or failed state and a
concrete decision when human judgment is required.

Client reconnection is view recovery, server restart is authority recovery, and
host loss is execution recovery. No one capability implies the other. A replacement
agent receives recorded state; it MUST NOT infer permission to repeat an uncertain
action from a conversation summary.
