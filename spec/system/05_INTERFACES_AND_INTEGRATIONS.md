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

## MQTT 5 messaging

Use [MQTT 5.0](https://docs.oasis-open.org/mqtt/mqtt/v5.0/os/mqtt-v5.0-os.html)
for asynchronous messaging across processes and servers. Use an existing conforming
broker and client implementation; Mitos defines payloads and domain behavior rather
than a new transport protocol. HTTP operations, artifact transfer, and terminal
streams retain their existing roles. In-process calls MAY use the same handlers
without a broker round trip.

Each server MUST have a configured broker endpoint and an isolated namespace scoped
by stable server, project, and mailbox IDs. Commands, events, and receipts MUST use
distinct topic classes. Independent servers MAY use different brokers; sharing a
broker MUST NOT share authority. Cross-server delivery requires an explicitly
authorized connection/bridge preserving destination identity and deduplication.

The versioned envelope MUST bind message ID, authenticated sender, destination,
type, causal/correlation references, deadline when applicable, payload digest, and
payload or artifact reference. Control operations additionally bind task/attempt,
generation, and expected revision as required. MQTT packet IDs and the DUP flag
MUST NOT replace stable application identity. Receipt topics MUST be authorized;
Response Topic and Correlation Data MAY carry reply routing and correlation.

Durable commands, events, and receipts MUST publish and subscribe at QoS 1. Receivers
MUST tolerate duplicates under the [runtime receipt contract](02_RUNTIME.md).
QoS 0 MAY carry disposable presence hints. QoS 2 MUST NOT be treated as exactly-once
task execution or external effects. Control messages MUST NOT use retained delivery;
retained snapshots and Last Will notifications, if used, are advisory state only.

Durable consumers MUST use stable, credential-bound Client IDs and configured
persistent sessions, with Clean Start false on resume and a bounded nonzero Session
Expiry Interval. Session loss MUST trigger reconciliation from Mitos records.
Message Expiry Interval MUST respect the domain deadline; expiry cannot silently
complete or cancel work. Receive Maximum, packet/payload bounds, queue limits, and
retry limits MUST provide explicit backpressure. MQTT flow control does not bound
application work already admitted to an inbox.

Remote connections MUST use TLS and scoped publish/subscribe authorization. Ingress
MUST authenticate sender claims through broker-enforced identity mapping or verified
credentials, not trust a sender field or topic name. MQTT access does not grant task
authority. One current coordinator generation owns a responsibility; shared
subscriptions MUST NOT elect coordinators or bypass runtime fencing. Arrival order
and reconnect order MUST NOT substitute for revision checks and causal references.

The journal remains authoritative. Broker persistence supplements delivery; durable
admission and handling require Mitos receipts.

The initial implementation uses Paho v0.23.0 and a configured external broker.
Topics are `mitos/v1/{destination}/{project}/{mailbox}/{kind}`. JSON envelopes are
signed with a persistent Ed25519 server key; peer keys and exact project/mailbox/
kind/type grants are pinned in configuration. Remote brokers require TLS; plaintext
is restricted to literal loopback addresses. Broker ACLs MUST separately restrict
subscriptions to the client's server namespace and publication to authorized peers.
Configuration changes and revocation take effect on server restart.

Supported handlers are `note/text` for `studio/operator` or an existing responsibility
at its current generation, and `command/service.event` or `event/service.event` for
`studio/events`. Service-event admission, subscriber dispatch records, and the
application receipt share one journal commit. A note receipt means durable inbox
admission, not that an agent has read it. Explicit note acknowledgment checks current
generation. Existing desk attention notes remain a separate compatibility interface.

The local HTTP/CLI adapter exposes status, send, outbox inspection, cursor-based inbox
reads, and note acknowledgment under `/api/messaging`. Payloads are capped at 64 KiB,
packets at 96 KiB, inbox reads at 50 records, and each journal projection at 10,000
identities. Receipts are retained; reaching capacity rejects new admission rather
than evicting deduplication history. Session expiry defaults to one day; retries
use persisted exponential backoff and default to 32 attempts. Unsent expired work is
`expired`; exhaustion after a possible send is `unknown`, reconcilable by a late
valid receipt. Deadline bounds are seven days. Unknown command types are rejected.

Servers on distinct brokers require an explicitly configured broker bridge routing
only the permitted namespaces. Automatic bridges, key discovery/rotation, arbitrary
remote process launch, and general control leases are not implemented by this adapter.

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

The current local project surface provides `/api/server` (stable identity, protocol,
capabilities), `/api/workspace-home` (aggregate status and decisions), and
`/api/projects/{project}/work` (paged tasks, goals, supervision, and execution metadata).
A selected `task` includes its report history and up to 200 attempts; current attempts
for the visible task page remain available. Terminal bytes use their existing stream.

`POST .../work` creates an assignment using a request key and payload digest.
The existing project session endpoint accepts a task assignment, launch key, and
expected revision. `POST .../work/{task}/{report|accept|cancel}` checks task revision;
reports additionally bind the current session, and acceptance checks the saved report
digest and criteria revision. Accepted/cancelled work is closed. Acceptance waits for
executor settlement and closed children and records the local operator's review.
Its scope is the frozen report, not independent verification of workspace changes.
Attempt readers identify this as `human_report_review`; shared journal records are
referenced by sequence/hash instead of copying project-wide payloads into each attempt.

`POST .../responsibilities/{id}/messages` saves bounded supervision notes; it does
not claim delivery, acknowledgment, or application. The HTTP-backed CLI exposes
`desk work --task ID` and `desk report-work --task ID --revision N --body TEXT`.
`X-Mitos-Server`, when supplied, must match the home identity. This local routing
check is not pairing or authentication. Capability discovery explicitly marks
remote placement, confinement, process reattachment, and durable control application
unavailable. Existing local session and batch endpoints remain supported.
