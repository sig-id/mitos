# 11 – Communication and decisions

## Durable control channels

Control messages MUST carry stable identity, sender, destination, causal references,
body revision, and relevant task/attempt/generation. Mailboxes MUST bound message
size, backlog, retention, and delivery attempts. Backpressure MUST be visible;
important work MUST NOT be silently dropped to keep a channel responsive.

Delivery, acknowledgment, and application are separate states. An adapter receiving
a message does not prove that an action was applied. Acknowledgment MUST bind the
current consumer generation. Applying a command MUST atomically persist the state
transition, receipt, and any resulting outbox entry.

Coordinator replacement MUST preserve pending messages and fence old consumers.
Messages MAY be delivered more than once; application MUST be idempotent. Cancellation
and expiration MUST have explicit receipts. Wait registration and wake-up use the
no-loss contract in [scheduling and dependencies](07_SCHEDULING_AND_DEPENDENCIES.md).

Topic discussions, terminal input, and notifications MUST NOT bypass control-channel
admission or be interpreted as grants. They MAY link to authoritative operations.

## Human decisions

A decision MUST identify the blocked work, causal request, current revision,
question, attempted action, constraints, supporting records, and available choices.
Each choice MUST explain its consequence and scope. A person SHOULD be able to
resolve it without reading the full conversation. A model MAY help compose the
explanation; deterministic policy owns whether the action is authorized.

Questions SHOULD ask for the missing fact or judgment rather than delegate the
entire coordination problem. Previously recorded authority SHOULD be used within
its bounds rather than requesting the same permission repeatedly.

Resolution MUST validate the caller's authority, decision revision, choice, and
value schema. The server MUST record resolver identity, channel, time, and receipt.
The first valid resolution wins. Repeated identical resolution MUST return that
receipt; conflicting resolution MUST be rejected and remain auditable.

## Silence and time limits

Silence MUST never create authority for an external or otherwise unauthorized
action. An unresolved decision remains pending unless its declared policy has a
safe, already-authorized default. A default and deadline MUST be explicit and
recorded; expiry MUST be distinguishable from a human resolution.

Missing credentials, facts, or judgments MUST not be invented. Unclassifiable
consequences MUST remain uncertain and subject to the required authority check.
Current local command decisions require explicit resolution.

Waiting for a person MUST retain deadlines, grants, and resource accounting.
Checkpointing MAY release compute only when the provider has actually stopped or
safely suspended execution and settled its resource custody. A UI label such as
“parked” does not establish that resources are free.

## Delivery and devices

Browser, CLI, and authorized device resolutions MUST operate on the same pending
decision and receipt. Notifications MAY provide a summary and link; notification
delivery, dismissal, or voice transcription MUST NOT approve the action.
Voice input is a draft until an authorized confirmation is recorded.

Reconnect MUST retrieve current decision state before submitting a cached response.
Revoked clients and obsolete revisions MUST fail. The narrow device API MUST NOT
inherit unrestricted terminal, artifact, or launch access from its ability to
resolve a decision. See [API and compatibility](14_API_AND_COMPATIBILITY.md).
