# 05 – Clients, applications, and integrations

## Client operations and security

Browser, CLI, and additional clients MUST operate on the same server-owned objects.
Mutations validate authentication, scope, expected revision, request identity, and
payload digest as applicable. Clients cannot invent resolver identity, process
ownership, or completion. Errors distinguish refusal, conflict, unsupported capability,
and unresolved outcome. [Runtime](02_RUNTIME.md) owns durable receipt semantics.

Across servers, references MUST include stable server identity and existing local
object ID. Cached views, streams, requests, and decision links retain that scope.
Clients MUST NOT route by name alone or reuse credentials across servers. Retries
return to the original receipt; endpoint changes require authenticated identity.

Remote access MUST use encrypted transport and scoped, revocable pairing. Revocation
rejects new operations and closes privileged streams. Relays and notification gateways
transport data without acquiring authority. SSH placement alone is neither a complete
runtime protocol nor isolation. The [architecture](01_ARCHITECTURE.md) owns multi-server
routing, ownership, and failure boundaries.

The current main server is loopback-only with Host checks and cross-origin mutation
rejection. Same-user access is not multi-tenant authorization. The optional HTTPS
device API permits scoped snapshots, decisions/resolutions, and configured voice
drafts, with pairing, rotation, revocation, and revision checks. It MUST NOT imply
arbitrary launch, terminal access, or unrestricted artifacts. Voice and notification
interaction require separate authorized confirmation before resolution.

## Terminal streaming

Output is a byte stream distinct from task state. The existing WebSocket contract
uses base64 bytes, absolute offsets, frames up to 64 KiB, and one unacknowledged frame
per connection. Renderer acknowledgment follows its write callback. Slow viewers
replay disk output without blocking the PTY. Reconnect resumes confirmed offset and
exposes retention gaps; final lifecycle follows draining retained output.

The bounded output-read API remains available. Current session/job output is capped
at 16 MiB. Input/resize are serialized and bounded; existing duplicate keys have an
in-memory window, not durable mutation receipts. Lost output disables input until
reconnect. Ambiguous keystrokes MUST NOT be automatically replayed. Rendering
acknowledgment is not command application. Shutdown cancels upgraded connections;
closing a client does not cancel its work.

## Agent adapters and continuation

Adapters MUST declare discovery, launch, input/output, cancellation, continuation,
and structured question/approval support. Unsupported operations fail explicitly.
The lifecycle MUST NOT depend on one vendor's conversation format or require an LLM
for deterministic work. Hooks retain source identity and distinguish observed intent
from admitted or completed action. Native credentials MUST NOT enter session metadata.

Resume uses a supported conversation identity with explicit execution binding.
Fork creates a distinct continuation and records its origin and shared history/files.
Handover provides another executor a portable assignment and selected records under
new authority checks. Fork MUST NOT imply a filesystem snapshot; handover MUST NOT
claim exact transfer of hidden model state. Interrupted execution requires inspection
and resource settlement before replacement; allowance and assignment persist.

## Applications and capabilities

An Application packages an entrypoint, execution/input contracts, required capabilities,
criteria, and optional services. A Capability supplies data, skills, tools, or hooks;
installation grants no authority. Applications use common admission, accounting,
observation, decisions, and acceptance. Required providers and dependencies MUST be
validated before launch; unsupported strict requirements MUST be refused.

Application and policy revisions freeze at admission. Updates MUST NOT change an
active task's verifier, workspace policy, or authority. Installation contents are
executable input subject to the provider's trust boundary.

The current local format is `app.json`; [runnable examples](../../examples/local-app/app.json)
show command argv and optional acceptance, compute, observation, and supervision.
Runners receive run identity, brief, and workspace via JSON stdin. Batch commands use
isolated HOME/TMPDIR, controlled Git configuration, no inherited provider credentials,
and 2 MiB combined output per command. This is environment hygiene, not confinement.
Absent strict compute requirements means recorded trusted-local posture, not an
unattended grant. [State and verification](04_STATE_AND_VERIFICATION.md) owns harvest
and saved-verifier behavior.

## Connectors and sinks

Connectors bring input into Mitos; sinks deliver results, notifications, or external
effects. Both MUST declare scope, authentication, payload bounds, retries, and
receipts. Incoming data cannot override grants, application, workspace, or policy
without a separately authorized mutation. Outgoing effects bind admitted action
and budget. Service ingress follows [orchestration](03_ORCHESTRATION.md).

Retries preserve identity. Without external idempotency or query support, ambiguous
results require reconciliation; transport success is not business-action success.
Integrations advertise actual capabilities without inheriting unrelated guarantees.
Hardware provisioning and packaging remain implementation concerns. Commerce and
replicated hosting require separate product decisions.

## Compatibility

Servers MUST expose protocol version and capabilities; clients refuse unsupported
operations and explain gaps. New fields, endpoints, and process-host messages need
versioned schemas and compatibility checks. Future prose does not create an endpoint.
Existing IDs, enum meanings, hashes, receipts, and API behavior MUST survive migration.

Cutover requires one writer per object, backups, restart-safe validation, and a
versioned migration receipt before irreversible format changes. Unmappable history
remains unresolved or unbound; it MUST NOT gain fabricated lineage. Read APIs MUST
bound output and provide stable cursors where applicable. Acceptance gates belong
to [delivery and conformance](06_DELIVERY_AND_CONFORMANCE.md).
