# 17 — Handheld decision connector (esp-notify)

Status: Mitos backend implemented and tested, 2026-09-20, including real Codex
and Claude native question/approval round trips. Physical esp-notify board
acceptance remains external. See [configuration and validation](../docs/DEVICE_API.md).
Consumer: `../esp-notify`, Waveshare ESP32-S3-Touch-AMOLED-1.8 firmware.
This document extends specs 10 (I/O), 15 (decisions), and 16 (autonomy).
Mitos implements all production backend functions described here in Go.

## Existing implementation and missing features

The current `internal/decision/card.go`, `docs/DECISIONS.md`, and
`internal/web/server.go` provide durable cards, local principal resolution,
first-writer-wins answers, and separate acceptance/application evidence.
Existing `/api/decisions`, `/api/runs/{id}/decision`, and
`POST /api/runs/{id}/decisions/{decision}` are local-operator boundaries.
They MUST NOT be exposed to the LAN or reused as remote authentication.

| Capability | Current Mitos | Required work |
| --- | --- | --- |
| Durable choices | Device retry/close/offered rewind share canonical authority | Hardware acceptance |
| Agent questions and approvals | Codex app-server and Claude hooks; durable source deduplication, response outbox, acknowledgement and session invalidation | Other native request types remain console-only |
| Value answers | Canonical string-schema validation; console/CLI/device values; multi-question native responses | Rich, secret and unsupported shapes remain console-only |
| Device identity | Separate TLS listener; durable scoped credentials; local pair/list/rotate/revoke CLI | Hardware provisioning and expiry/rotation UX |
| Device snapshots | Bounded complete projection, stable pagination, revisions, recent outcomes | Hardware display acceptance |
| Voice | Strict WAV ingest; cancellable HTTP/executable providers; draft-only option matching and schema-validated text | Select/configure the STT provider; hardware capture acceptance |
| Network retry | Atomic command/body digest, canonical answer and acceptance receipt; durable replay | Hardware journal/reconnect acceptance |

Do not alter implemented-status claims until the corresponding acceptance tests
pass. This specification does not request a second backend in esp-notify.
Its Python fixture is strictly development infrastructure.

## Product flow

1. Operator pairs a device in an authenticated local Mitos session and provisions
   the HTTPS origin, device token, Wi-Fi, and optional private root CA over USB.
2. Device polls a small snapshot every 3 seconds while connected. It shows pending
   requests, recent decision outcomes, and active/recent runs. Offline data is
   explicitly stale and cannot authorize a new answer.
3. Operator opens a card and reads the question, context, impact and consequences.
4. Operator selects an offered choice or holds the talk button. Mitos transcribes
   audio and proposes a draft bound to the exact decision/run/revision.
5. Device shows the transcript, proposed text value and choice. A separate touch
   on Confirm sends the answer. STT or a model never executes a decision.
6. Device distinguishes accepted, applied, and application_blocked. It persists
   only an already-confirmed command until a matching receipt is received.
   After a timeout/reboot, explicit Check / retry resends exactly that command.

No wake word, continuous listening, TTS, mobile intermediary, or third-party
voice account is required. STT provider selection and credentials belong to
Mitos; the embedded device has no model-provider credentials.

## Identity and transport

A separate TLS listener/connector mounts `/api/device/v1`. A TLS terminating
proxy is acceptable only with an authenticated protected hop to the connector.
Do not relax the existing loopback server's boundary. Every endpoint checks
`Authorization: Bearer <opaque-device-token>` against a server-side binding
(device ID, operator principal, tenant/project scope, permissions, revocation).
MAC addresses, supplied resolver fields and transport labels are not identity.
Use random tokens with at least 256 bits entropy; store token hashes, never
plaintext. Initial authenticated local pairing, list/revoke, token rotation and
expiry must be available through Mitos CLI/console before deployment.

Scopes: read authorized cards/runs, submit decisions offered to that operator,
create voice drafts. No arbitrary execution, run creation, or admin access.
TLS server name and certificate chain verification are mandatory on the device.
Return JSON with Content-Type application/json and Cache-Control: no-store.
Redirects are unsupported. A 401/403 never falls back to local-operator identity.
Authorize before reading/projecting a card or accepting audio. Apply per-device
rate and concurrency limits, and bound request and response sizes.

## Wire contract v1

JSON uses exact fields below. Unknown fields, duplicate keys, missing required
fields, and incompatible versions are errors. All responses below include
`"version":1`. Identifier strings: 1–96 ASCII bytes `[A-Za-z0-9_.-]`.
Revisions are opaque identifiers, changed on every material card/options/input,
resolution or authorization change. They bind an immutable reviewable request.
All null fields must be explicit. The device uses exact ID equality.

Initial font profile is printable ASCII plus LF/TAB. Mitos MUST NOT silently
transliterate, clip, replace glyphs or summarize away authorization scope to
make a card fit. If a request cannot be represented completely within these
bounds, return 422 `display_unsupported` and route it to the console; later
protocol versions can negotiate fonts and detail retrieval. Errors cannot
make an unreviewable request actionable. A blocked projection must not suppress
notifications in other configured Mitos sinks.

### GET /api/device/v1/snapshot?cursor=<url-encoded opaque cursor>

Empty cursor starts a new view; `next_cursor` is empty at the end. A cursor
(max 256 bytes) binds tenant, sort position and a stable bounded scan. Mitos
must support polling any cursor, expired cursors return 410. Clients can return
to Requests/Activity to restart from the first page. Sort pending decisions
first, then recent decisions; include active runs before recent terminal runs.
Keep recent resolutions for at least 24 hours. Pagination must eventually expose
every authorized pending request and active run; never silently truncate totals.

Response <= 49152 bytes, <= 8 cards and <= 8 runs. Reduce page count as needed
to respect the byte limit. Pending total is independent of the current page.

```json
{
  "version": 1,
  "pending_total": 1,
  "next_cursor": "",
  "cards": [{
    "decision_id": "decision_1", "run_id": "run_1", "revision": "rev_1",
    "title": "Release agent / project",
    "question": "Publish the reviewed release?",
    "context": "Attempted: Built and checked artifacts.\nBlocked by: Publication needs approval.\nEvidence: release/checks.txt",
    "impact": "External action. Public release in owner/project. No spend. No automatic default.",
    "state": "pending", "input": "none", "application_error": "",
    "options": [
      {"id": "approve", "label": "Publish", "consequence": "Artifacts become public."},
      {"id": "deny", "label": "Keep draft", "consequence": "Nothing is published."}
    ]
  }],
  "runs": [{"run_id": "run_1", "title": "Release", "status": "waiting_for_human", "summary": "Awaiting publication decision."}]
}
```

Text limits in bytes: title 160, question 800, context 4096, impact 2048,
application_error 512, option label 120, consequence 1024, run summary 1024.
There are exactly 2 or 3 unique options. `context` folds canonical attempted,
blocked_by and evidence references. `impact` must include reversibility,
blast-radius paths/destinations/spend, cost-of-wrong and default/expiry policy.
It must not substitute model narration for durable source evidence. Do not
present remote scripts or agent-generated descriptions as trusted UI controls.

Card state: pending, accepted, applied, application_blocked, expired, superseded.
Run status: queued, running, waiting_for_human, blocked, succeeded, failed,
cancelled, interrupted. Backend maps canonical run state explicitly.
`input`: none, text, unsupported. Text is a schema-validated string value
required for each option in this v1 profile. Shapes with option-specific
required values or structured/secret inputs are unsupported on this device.
Sensitive input such as passwords MUST route to the console, never the STT path.
Resolution state comes from the log, never inferred from a run being active.

### POST /api/device/v1/voice-drafts?decision_id=...&run_id=...&revision=...

Content-Type: audio/wav. Exactly mono PCM signed 16-bit little endian, 16000 Hz;
0.2–12 seconds, <= 384044 bytes (44-byte WAV header plus samples). Reject wrong
format, malformed WAV, extra/truncated samples and overlong input before STT.
Firmware captures only while held, auto-stops at 12 seconds, scrubs its buffer,
and does not write audio to flash. No background microphone upload.

Validate principal, pending state, revision, and supported non-secret input
before STT; recheck state before returning. Execute STT through a configured
provider interface with a deadline and cancellation. Return within 20 seconds
or 503; the device does not poll an asynchronous transcription job in v1.
Suggested interface: Transcribe(ctx, PCM, locale) -> transcript.
Optional interpretation can map transcript to *offered* option IDs and a string
value, but MUST NOT modify or execute a decision. Do not interpret arbitrary
spoken text as commands. An uncertain/unmatched option returns empty `choice`.
Uncertain or silent transcription returns 422, not a guessed approval.

```json
{"version":1,"decision_id":"decision_1","run_id":"run_1","revision":"rev_1","choice":"deny","transcript":"Keep the draft","value":null}
```

Transcript/value <=1024 bytes each. `value` is null for input none; for text it
is the proposed answer, separately displayed before confirmation. A missing
choice requires explicit user selection. Provider errors and silence do not
create a durable decision. Do not retain audio by default or log raw transcripts;
only the confirmed choice/value enters the resolution record. Disclosure of
external STT routing and any opt-in retention policy belongs in Mitos pairing.

### POST /api/device/v1/resolutions

Content-Type: application/json, max 4095 bytes. Request:

```json
{"version":1,"command_id":"random_128_bit_id","decision_id":"decision_1","run_id":"run_1","revision":"rev_1","choice":"deny","value":null}
```

The device generates a random 128-bit command ID and durably saves the exact
command before the first POST. There is at most one pending confirmed command.
The device never queues a new approval while offline or while a receipt is
uncertain. Unconfirmed drafts disappear on disconnection, material revision
changes or restart. Confirmed commands survive restart; operator explicitly
retries the same identity and body after reconnect.

Server processing, in this order:

1. Authenticate device, tenant, permission and revocation on every retry.
2. Look up durable `(device_id, command_id)`. An identical body returns the
   original acceptance receipt even if the card revision/state changed since
   acceptance. A different body returns 409 idempotency_conflict with no effect.
3. For a new command, validate the exact run/decision/revision, still pending,
   not expired/superseded, offered option, string schema and principal authority.
   Recheck under the same serialized transaction as resolution.
4. Atomically durably record command identity/body digest, canonical resolution
   and receipt before acknowledgement or application. Reuse existing Mitos
   first-writer-wins resolution; a device connector never executes independently.
5. Apply through existing runtime mechanisms. A crash at any boundary must not
   create a second retry child or overwrite the first resolution. Do not leave
   a window with a resolution but no recoverable command receipt.

Concurrent console/device answers: exactly one wins. Later conflicting commands
return 409. Canonical `resolver`, tenant, time and `via=esp-notify` are supplied
by the server; source device and originating agent request are audit metadata.
An accepted but blocked application is not failure to accept. Preserve receipts
for the decision's durable lifetime, or preserve a tombstone that prevents
re-execution and lets an operator recover the original result.

200/202 response:

```json
{"version":1,"command_id":"random_128_bit_id","decision_id":"decision_1","state":"accepted","result_run_id":""}
```

Receipt state: accepted, applied, application_blocked. Result run may be empty
until applied; retry application may produce a child run. Firmware checks both
command_id and decision_id before erasing its journal. It never calls a request
complete solely because the socket write or HTTP status succeeded.

Errors are `{"error":"machine_code"}`: 400 malformed, 401 unauthenticated,
403 revoked/forbidden, 404 endpoint/resource unavailable, 409 stale/conflict,
410 expired, 413 body limit, 422 unsupported display/input/schema/audio,
429 rate limited, 503 STT/backend unavailable. No mutation on any definitive 4xx.
Timeout, malformed receipt, 401, 404, 413, 429 and 5xx leave firmware's confirmed
journal unresolved. 400/403/409/410/422 release it with an explicit error and
require a new snapshot before a new answer. Authentication revocation can
prevent retrieving an old receipt; the console remains the recovery surface.
No unconfirmed draft, network reconnect, or expiry creates authorization.

## Agent adapters

Add a normalized ingress for harness questions and approval requests. Bind
source adapter, session, upstream request ID, run, tenant, and causal evidence;
deduplicate source retransmissions. Map executable upstream choices to canonical
card options without weakening permission or default rules. Only allow string
questions on the initial value path; richer schemas stay console-only.

On canonical resolution, durably deliver the answer to the originating waiting
session. Delivery failure is application_blocked and retryable, not permission
to reinterpret or create a new answer. Session termination/supersession must
invalidate unresolved cards. Untrusted agent text cannot choose a principal,
set a token, change tenant, or supply an authorization revision. Build actual
adapters to the harnesses used by the operator; do not claim integration merely
because the handheld can render fixture cards.

## Acceptance gates for Mitos implementation

- Remote listener never obtains LocalOperator; wrong/revoked token and wrong
  tenant cannot read, transcribe or resolve any card; rotation takes effect.
- Pair/revoke lifecycle, TLS chain/name checks and authorization audit are tested.
- Complete bounded snapshots paginate >8 cards/runs without losing requests;
  expired cursor restart and rejected oversized/non-displayable requests tested.
- Typed source question and approval round-trip back to the actual waiting
  harness, including session exit, duplicate source request and failed delivery.
- STT provider success, silence, ambiguous answer, unsupported value, wrong
  audio, size/rate limit, provider timeout and revocation during STT tested.
- Voice response alone cannot create approval_resolved or execute anything.
- Stale revision, expired card, invalid choice/value and console/device races
  cannot authorize a new action. Schema validation occurs in the authority path.
- Drop connection after durable commit: byte-identical command retry returns
  original receipt. Same ID/different body rejected. Replay after server restart
  and crash between acceptance/application produces one outcome/child run.
- Acceptance vs application-blocked vs applied exposed correctly and persists
  through restart; no secret/audio leakage in logs, projections, or error bodies.

Suggested delivery order: authenticated device boundary and pairing; bounded
read projection; idempotent resolution; upstream question adapters/string values;
STT draft service; hardware acceptance against esp-notify. Mitos owns production
backend tests; esp-notify owns parser/state-machine and board acceptance tests.
