# 11 – Frontend and client contracts

Status: normative product contract. [Workspace](WORKSPACE.md) defines primary
navigation and interaction. [Product](02_PRODUCT.md) defines outcomes;
[durable runtime](../system/DURABLE_RUNTIME.md) defines authority and recovery.
Historical batch-console rationale is retained in [ADR 0002](../ADRs/0002_BATCH_CONSOLE.md).

## 1. Surfaces and authoritative state

All clients MUST use the same backend operations and authorization rules. Screens
MUST render authoritative state with its revision; local optimistic state MUST be
visibly pending and MUST NOT establish completion or permission. Caches and drafts
MAY be local. Credentials MUST NOT be embedded in shareable object identifiers.

The project workspace MUST be the primary interactive surface. It MUST expose
supervisor, workers, actual sessions, task/attempt lineage, topics, changes,
resources, and decisions. Batch history, evidence reports, services, and installed
applications MUST remain accessible. A native terminal MUST remain available where
structured rendering cannot faithfully represent an interaction.

A terminal or chat view MUST name its project, execution host, assignment, and
executor. Switching views MUST NOT silently launch another process. When an adapter
requires a new native session, the UI MUST disclose that continuation creates a
new execution and link it to the source.

## 2. Mutations, connection loss, and streaming

Clients MUST preserve request IDs for retriable structured operations and query
receipts after ambiguous responses. They MUST NOT blindly resubmit raw terminal
input. Sending, accepted, delivered, acknowledged, and applied MUST be distinct
where relevant. Server errors MUST state whether effects are known, absent, or
unknown and provide a recovery action.

Output MUST resume by stable offsets or explicitly report truncation. Slow clients
MUST have bounded buffers. Navigation and disconnect MUST leave work running unless
the user requests cancellation. Pending drafts, reading position, keyboard focus,
and expanded evidence MUST survive routine updates where possible.

Clients MUST negotiate capabilities and disable unsupported actions with a reason.
Remote host unreachability MUST NOT appear as task failure or process exit.
Notifications MUST navigate to current authoritative state; dismissing them MUST
NOT answer a question or acknowledge a control message on behalf of an agent.

## 3. Decisions, acceptance, and reader boundaries

A decision UI MUST present the exact actionable revision, scope, choices,
consequences, and required evidence. Submissions MUST be explicitly confirmed and
bound to that revision. Stale, revoked, or already-resolved requests MUST not remain
actionable. Silence MUST NOT authorize an external action.

Status MUST distinguish worker-reported success, execution exit, verification,
and accepted completion. Reports MUST identify result revision, verifier evidence,
actual provider/isolation coverage, and unresolved effects. Missing measurements
MUST remain unknown rather than displayed as zero or a fabricated success rate.

Authorization and redaction MUST be enforced by the backend, not hidden UI controls.
Commercial proof is a restricted reader cut under
[Facilitator](../system/08_FACILITATOR.md); private operator rationale, credentials,
and seller internals MUST NOT enter that response. Shared report components MAY
render distinct cuts, but MUST NOT fetch a broader cut and hide fields locally.

## 4. Accessibility and mobile use

Core task and decision journeys MUST support keyboard navigation, visible focus,
semantic labels, and reduced motion. State MUST not rely on color alone. Narrow
screens MUST retain project/host context and reachable pending decisions. Updates
MUST NOT overwrite an unsent answer or move focus without user action.

Mobile and handheld capabilities MAY be narrower than desktop/browser capabilities.
The handheld v1 protocol MUST retain its existing read/resolve/voice boundary.
Unsupported content MUST be rejected explicitly, not clipped into a misleading
approval. Remote control expansion requires a versioned protocol and grants.

## 5. Validation

Client acceptance MUST cover two simultaneous clients observing one execution,
reconnect without duplicate input or launch, stale decision rejection, revoked
access, truncated output, keyboard-only decisions, narrow-screen review, and
explicit unknown remote state. These requirements do not claim native desktop or
mobile applications currently exist. The Go templates/HTMX and terminal frontend
remain supported implementations of this contract.
