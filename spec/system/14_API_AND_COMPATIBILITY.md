# 14 – API and compatibility

## Shared operations

Browser, CLI, and additional clients MUST refer to the same server-owned objects.
Mutations MUST validate authentication, scope, expected revision, operation identity,
and payload digest as applicable. A client MUST NOT invent resolver identity,
process ownership, or completed status. Errors MUST distinguish refusal, conflict,
unsupported capability, and an outcome still requiring reconciliation.

The server MUST expose its protocol version and supported capabilities. Clients
MUST refuse unsupported operations and explain implementation gaps. New fields,
endpoints, or process-host messages MUST have versioned schemas and compatibility
checks. Spec prose naming a future operation does not create a shipped endpoint.

Existing Task/Run/Session identities, enum meanings, artifact hashes, decision
receipts, and API behavior MUST survive consolidation and migration. An incompatible
change requires an explicit versioned transition and migration procedure.

## Local access

The current main server binds to loopback, validates Host headers, and rejects
cross-origin mutations. It runs under the operator's identity; it MUST NOT be
advertised as a multi-tenant authorization boundary. Broad remote exposure requires
the paired, scoped access contract in [server and clients](02_SERVER_AND_CLIENTS.md).

Read APIs MUST bound output and preserve stable pagination/cursors where applicable.
Long-lived connections MUST be cancelled explicitly on shutdown. Client disconnect
MUST NOT implicitly stop execution or resolve decisions.

## Terminal streaming

Terminal output is a byte stream separate from task state. The current WebSocket
contract uses base64 bytes, absolute offsets, frames up to 64 KiB, and at most one
unacknowledged frame per connection. Renderer acknowledgment follows its write
callback. A slow viewer MUST replay retained disk output rather than block the PTY.

Reconnect MUST resume from the confirmed offset and expose any retention gap.
Final lifecycle state MUST follow draining retained output. The bounded output-read
API remains available independently of the live stream. Output retention limits
MUST be published; current interactive session/job output is capped at 16 MiB.

Input and resize requests MUST be serialized and bounded. Existing input duplicate
keys have an in-memory window, not the general durable mutation guarantee. A lost
output connection disables input until reconnection. Ambiguous keystrokes MUST NOT
be replayed automatically. Rendering acknowledgment is not command application.

## Restricted device API

The optional HTTPS device API permits scoped snapshots, pending decisions,
authorized resolutions, and configured voice drafts. Pairing, credential rotation,
and revocation MUST preserve the device's restricted scope. Responses MUST carry
sufficient revision information to reject stale decisions.

This API MUST NOT expose arbitrary launch, terminal input/output, unrestricted
artifact reads, or the whole workspace by implication. Voice transcription and
notification interaction MUST remain distinct from confirmed resolution. A full
remote or mobile client requires the broader negotiated client contract.

Hardware rendering, sound feedback, Wi-Fi provisioning, and terminal-renderer
benchmarks do not define product scope. Implementation-specific operational
instructions MAY remain next to the relevant tools or deployment assets.

## Migration

Cutover MUST have one writer per object, preserve historical records, and support
restart-safe validation. Backups and versioned migration receipts MUST precede
irreversible format changes. Records that cannot be faithfully mapped MUST remain
explicitly unresolved or unbound rather than receive fabricated lineage.
[Delivery and conformance](15_DELIVERY_AND_CONFORMANCE.md) defines migration gates.
