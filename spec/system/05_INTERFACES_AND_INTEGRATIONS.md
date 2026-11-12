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

## Application and worker server contract

This is a target contract, not a declaration of existing endpoints. Application and
worker protocols MUST be independently versioned and capability-negotiated. Clients
MUST reject unsupported operations. The application interface MUST support idempotent
work admission, status and result reads, cancellation, durable input/event submission,
and receipt reconciliation with bounded cursor-based reads.

Remote application access MUST authenticate service principals with scoped, revocable
grants; browser/device pairing is not the only authentication mode. Tenant and project
scope MUST be validated for every operation, context read, artifact, event, and receipt.
Caller-supplied identity fields MUST NOT establish authority. Admission MUST enforce
per-tenant capacity and declared budgets as well as global pool limits. Credentials
and private context MUST NOT leak through another tenant's logs or notifications.

The worker protocol MUST support capability registration, eligible work claims,
authorized context retrieval, progress, lease renewal, checkpoint submission, result
submission and durable acceptance receipts. Each attempt MUST bind task, Run, worker
incarnation, authority generation, immutable input versions, acceptance policy and
limits. A successful claim MUST be durably committed before execution is authorized.
Mutations MUST validate the current binding and deduplicate stable request identities;
results MUST revalidate consumed context versions and current permissions. Stale
results remain inspectable evidence but MUST NOT publish an accepted result.

Lease expiry withdraws authority to commit; it does not prove a worker has stopped.
Replacement MUST obey the runtime's resource-custody rules. External actions MUST
pass through an enforcement boundary that validates current authority and persists a
stable operation identity before dispatch. An ambiguous action outcome MUST retain
its reconciliation hold across worker replacement. Direct worker credentials that
outlive revocation cannot establish safe automatic replacement.

Context MAY be served by an application-owned API using an attempt-scoped delegated
grant. The application remains the authority for domain visibility. Context references
MUST bind versions and permissions, and revocation MUST be enforced at retrieval and
publication. Checkpoints MUST be portable application state with declared schema and
provenance; they MUST NOT promise transfer of hidden model or framework state.

MQTT work notifications carry bounded references, not confidential conversation
payloads. Shared subscriptions MAY distribute wake notifications among compatible
workers; the claim API determines ownership. Missed notifications MUST be recovered
by bounded scans of durable eligible work. Broker acknowledgment is not a work claim
or result acceptance. Results sent through MQTT require the same ingestion validation
and durable receipt as API submissions.

The application declares a serialization key for conversational work and versions
of consumed preferences or profile data. Admission MUST serialize conflicting work;
result publication MUST revalidate versions across the application/runtime boundary.
Independent background tasks MAY run concurrently. Waiting MUST durably register its
wake condition and checkpoint before releasing worker capacity, without losing an
answer arriving during registration. Wake-up creates or resumes an authorized attempt
under current policy; it does not revive an expired worker grant.

## Adaptive graph and controller interface

The target `work-graph/v1` profile defines semantic operations below, not currently
available HTTP routes or MQTT handlers. Servers MUST advertise implemented profile
versions, executor adapters, supported operations, and finite admission/read limits.
Unknown versions, operations, enum values, and fields MUST be rejected, not ignored.
HTTP, CLI, in-process, and MQTT adapters MUST resolve to the same typed handlers,
authority checks, canonical request identity, and receipts.

| Operation | Required input | Required observable result |
|---|---|---|
| Create graph | Root assignment, goal/policy revision, activation receipt, limits, initial expansion scope or defined members | Graph/root IDs and receipt; no implied worker launch |
| Read graph | Graph ID, snapshot revision or cursor, bounded membership/record selection | Revision-consistent page, continuation cursor, explicit truncation, obligations, blockers, and fact references |
| Preview amendment | Complete amendment envelope and proposed operations | Validation findings at an identified snapshot; no reservation, inference, or mutation |
| Apply amendment | The envelope below | Atomic commit/rejection receipt with resolved IDs and resulting structural revision |
| Request decision | Boundary/trigger identity, frozen candidates, projected evidence, model/policy revision, deadline and allowance | Durable request receipt and eventual validated answer/refusal/unknown outcome |
| Apply selected proposal | Decision receipt, selected candidate digest, amendment identity and preconditions | Original amendment receipt or visible conflict; never an implicit second inference |
| Inspect operation | Original request identity | Pending, committed, rejected, or unresolved outcome and associated effects |
| Observe graph | Graph/cursor and bounded subscription | Ordered committed changes with gap detection and snapshot reconciliation |

The amendment envelope MUST contain `version`, home/project/graph identity,
`request_key`, expected graph structural revision, policy revision, proposer identity
bound to authentication, controller generation when applicable, deadline, relevant
fact/specification read set, and a bounded ordered operation list. The handler computes
a canonical payload digest. Each referenced artifact binds identity, hash, and size;
proposal-local task references are valid only within their containing transaction.
Evidence and rationale MUST be bounded inline values or authorized immutable references.
The initial wire implementation MUST publish a schema and canonical digest test vectors,
including Unicode, duplicate-key rejection, numeric normalization, and reordered fields,
before advertising this profile. Existing receipt digests MUST NOT be reinterpreted.

Execution inputs MUST reference a registered adapter/schema revision, an exact executor
or bounded selector, assignment or typed arguments, context/input bindings, acceptance
policy, workspace/resource requirements, and limits. Agent-template specialization
references its immutable base revision and changes.
The [agent-library profile](07_AGENT_LIBRARY.md#interface-and-delivery-requirements)
owns definition discovery, registration, specialization and binding reads. Definition
publication is distinct from executable adapter installation and from process launch. Generated scripts and tool sequences
MUST bind content digests as execution input. An adapter's schema permits invocation
arguments; it does not imply permission for all representable operations.

Read APIs MUST expose why a task is blocked and which decision, dependency, resource,
controller obligation, or reconciliation would unblock it. Clients MUST be able to
trace added/superseded tasks to their proposal, trigger, and decision evidence without
reading an entire transcript. Cached previews MUST be revalidated on commit. Cursors
and notifications MUST NOT confer authority; a transport receipt is not graph commit,
process launch, or acceptance. Model probability and an authored rationale MUST be
labeled separately from the authoritative reason an amendment was permitted.

## Native-agent observation contract

Managed execution MUST support ordinary native agent output without a mandatory
Mitos response grammar. Adapters MAY consume native structured events/tool calls,
terminal text, explicit report submission, and produced artifacts. They MUST preserve
source evidence and emit runtime-owned observations binding task, attempt, process
incarnation, sequence/cursor, time, source kind, and content/artifact references.
Presentation formatting or extraction MUST NOT discard the retained source.

Deterministic extraction MAY identify known native events. Model interpretation MAY
extract claims, candidate work, or likely requests from unstructured output, but its
record MUST identify the interpreter/model revision, source span/digest, target schema,
uncertainty, and interpretation policy. Interpretation is a separately accounted
operation with a receipt. Raw text, extracted JSON, role labels, and apparent tool-call
text MUST NOT impersonate authenticated commands or change graph state directly.
Malformed/ambiguous interpretation produces an observation or visible question, not
an implicit launch, continuation, approval, or successful completion.

Explicit structured tools MAY request graph operations under their caller's grant.
Ordinary replies MUST NOT need to call them. A controller may propose an amendment
based on observed text through the same validation path. An adapter reporting process
exit proves only the observed lifecycle event; interpreting "done" yields a completion
claim. Neither proves that required artifacts exist or that acceptance passed.

Context assembly MUST provide the assignment, relevant knowledge and artifact
provenance, current accepted facts, permitted capabilities, and outstanding questions.
It SHOULD hide transport envelopes, revision bookkeeping, and graph command syntax
from an executor unless that executor explicitly performs a controller/tool role.
Templates and context policies MUST declare source permissions and truncation behavior.
Model interpretation and planning MAY use authorized source content under their own
bounded data-disclosure policy; they MUST NOT silently expand the existing batch
choice-hook projection to include transcripts, credentials, or whole workspaces.
The batch decision provider profile below remains independently versioned and limited.

## MQTT 5 messaging

Use [MQTT 5.0](https://docs.oasis-open.org/mqtt/mqtt/v5.0/os/mqtt-v5.0-os.html)
for asynchronous messaging across processes and servers. Use an existing conforming
broker and client implementation; Mitos defines payloads and domain behavior rather
than a new transport protocol. HTTP operations, artifact transfer, and terminal
streams retain their existing roles. In-process calls MAY use the same handlers
without a broker round trip.

Each server using MQTT MUST have a configured broker endpoint and an isolated namespace scoped
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

The configured runtime persistence remains authoritative (the journal in the current
implementation). Broker persistence supplements delivery; durable admission and
handling require Mitos receipts.

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
show command argv and optional acceptance, compute, observation, supervision, and decision-model policy.
Runners receive run identity, brief, and workspace via JSON stdin. Batch commands use
isolated HOME/TMPDIR, controlled Git configuration, no inherited provider credentials,
and 2 MiB combined output per command. This is environment hygiene, not confinement.
Absent strict compute requirements means recorded trusted-local posture, not an
unattended grant. [State and verification](04_STATE_AND_VERIFICATION.md) owns harvest
and saved-verifier behavior.

### Decision-model provider profile

The optional `decision_model` object in a local application declares inference at
selected batch boundaries. Omission makes no model requests. Every field below
except `api_key_env` is required in the profile; zero thresholds explicitly disable
that particular threshold check. Implementations MUST reject unknown protocols and
hooks, duplicate hooks, invalid URLs, and values outside the runtime bounds.

```json
{
  "decision_model": {
    "protocol": "systemone",
    "endpoint": "http://127.0.0.1:8000/v1/systemone",
    "model": "english",
    "hooks": ["before_segment"],
    "instructions": "Continue only when the supplied task is sufficiently specified for the declared segment. Otherwise escalate for review.",
    "timeout_ms": 15000,
    "max_calls": 8,
    "min_confidence": 0.7,
    "min_probability": 0.85
  }
}
```

This is a manifest fragment for a separately operated local Laya-compatible server;
Mitos does not install or launch inference servers. For Jev, select the desired
model and full endpoint `https://api.typesafe.ai/v1/systemone`, and set
`api_key_env` to `TYPESAFE_API_KEY`. For OpenAI use `protocol: "openai_decisions"`,
`endpoint: "https://api.openai.com/v1/decisions"`, the desired supported model
(for example `gpt-6-luna`), and `api_key_env: "OPENAI_API_KEY"`. Configure the named
environment variable on the Mitos server, not in the manifest or worker command.
Equivalent services may use an explicitly configured compatible endpoint.

| Adapter | Outbound shape | Inbound shape |
|---|---|---|
| `systemone` | `model`, JSON `state`, `questions.action` with `type: "choice"`, `instructions`, and string-valued `criteria` map | `answers.action` with `type`, `choice`, `confidence`, and `probabilities` map |
| `openai_decisions` | `model`, `input` containing serialized state, one `questions` array item named `action`, `type: "choice"`, `instructions`, and `choices` array of value/description objects | One named `answers` array item, with a choice and confidence; probabilities are an array of value/probability pairs |

The normalized answer retains the returned model identity, choice, confidence,
probabilities, and optional input/output token usage. A configured alias is allowed;
pin a provider model revision when reproducibility matters. A changed provider alias
cannot make stochastic inference reproducible, so replay uses saved answers.
Only string-valued Choice is implemented for runtime control. Predicate/Noul, Score,
images, batch inference, streaming, tool calling, and generated explanations are not
part of this profile. Future types require explicit mappings and validation.

The runtime supplies the question ID and options. Projected state contains Task/Run
IDs, hook, segment, bound target, application ID/entrypoint/description, title, intent,
failure signal, latest acceptance
metadata, up to eight recent artifact descriptors, remaining call allowance, and
execution deadline. It excludes workspace contents, raw output, transcripts, event
payloads, environment, and server credentials. User-supplied title and intent are
sent verbatim; this projection is not an automatic secret scrubber. Enabling a remote endpoint sends that projection
and the configured question to that provider. Input and instructions are data;
neither can change runtime authority. The HTTP request and response are each limited
to 64 KiB; instructions are limited to 4000 bytes. Oversized input is refused rather
than silently truncated.

Endpoints MUST use HTTPS, except HTTP to a literal loopback IP for local inference.
Userinfo, query strings, and fragments are refused. Credentials are resolved only
at call time and sent as Bearer authorization; only their environment variable name
is persisted. Redirects and automatic retries are disabled. `X-Client-Request-Id`
carries the durable local request ID for correlation; it does not assert that a
provider deduplicates requests. Configuring an endpoint is a trusted local application
operation, not a worker-selected destination. This profile is not a credential broker
or a guarantee of remote data retention.

Inspect the frozen policy and `model_decisions` through the existing Run JSON API
(`GET /api/runs/{id}`) or `mitos status RUN_ID`. The journal records
`model_decision_requested` and `model_decision_completed`. Human review continues to
use the existing decision inbox and resolution API. There is no model-settings UI
or automatic model resolution of human cards.

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
