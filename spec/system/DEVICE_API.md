# Handheld device API

Status: Normative local behavior and API contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

Mitos exposes the esp-notify v1 device contract on an optional, separate HTTPS
listener. It supports native Mitos recovery decisions, Codex and Claude waiting
questions and one-shot approvals, schema-validated text answers, durable command
receipts, paginated activity, and voice drafts. The local Studio listener retains
its loopback and cross-origin rules.

## Start and pair

Use a certificate whose subject alternative names include the hostname or IP
that the handheld will connect to. Supply its certificate chain in the PEM file.
Provision the issuing private CA to the handheld when using a private PKI.

```sh
mitos serve --addr 127.0.0.1:4646 \
  --device-addr 0.0.0.0:8443 \
  --device-cert /path/to/device-chain.pem \
  --device-key /path/to/device-key.pem

mitos device pair --name 'Desk handheld' --ttl 720h
mitos device list
mitos device rotate --ttl 720h DEVICE_ID
mitos device revoke DEVICE_ID
```

Pairing and rotation return JSON with a `device` object and a `token`. Transfer
the token, HTTPS origin, Wi-Fi settings, and any private root CA to esp-notify
over USB. Mitos persists only the token's SHA-256 digest. Tokens have 256 bits of
randomness, expire after the selected lifetime (maximum 366 days), and bind the
pairing operator, tenant, and permissions. Rotation immediately invalidates the
old token and any review revisions issued under it; revocation also prevents
receipt retrieval. The console remains available for recovery.

The CLI uses the running Studio's local `/api/devices` administrative endpoints.
This Studio pairs its authenticated local operator in `local/studio`; remote
requests obtain their authority exclusively from the saved device binding.
Use `--resolve=false` to pair a read-only device. Voice is disabled in newly
paired credentials unless `--voice` is supplied.

The remote listener exposes only:

| Method | Path | Permission |
| --- | --- | --- |
| GET | `/api/device/v1/snapshot` | read |
| POST | `/api/device/v1/resolutions` | resolve |
| POST | `/api/device/v1/voice-drafts` | voice |

The device listener additionally serves the static Wi-Fi sender assets under
`/wifi-setup/`. Those public GET/HEAD resources have no credential-submission API
and no Studio access. See [deployment instructions](../../deploy/wifi-setup/README.md).

Every device API request requires `Authorization: Bearer TOKEN`. Responses use JSON and
`Cache-Control: no-store`. There are no redirects or local-identity fallbacks.
The implementation requires TLS at the connector itself; an HTTP proxy hop is
not supported. The remote listener cannot create runs, execute arbitrary
commands, read artifacts, or administer device credentials.

## Snapshots and answers

[The v1 specification](17_HANDHELD_DECISION_CONNECTOR.md) defines the exact
wire fields. A snapshot includes pending requests before recent outcomes and
active runs before recent terminal runs. Each page contains at most eight cards,
eight runs, and 49,152 bytes. The independent `pending_total` covers the full view.
Follow `next_cursor` until it is empty. Polling an existing cursor returns the
same stable view; restart from an empty cursor to see current state.

Cursors expire after five minutes or server restart (`410 cursor_expired`). They
are tied to the paired device and its authorization revision. Unchanged
first-page polls reuse a view. The connector retains at most four distinct views
per device, 64 globally, and 32 MiB of encoded view data. An individual view is
limited to 16 MiB; capacity failures are explicit, never silent truncation.
Decisions and runs remain available in recent activity for at least 24 hours
following their resolution/application or last run update. Unresolved requests
remain visible regardless of age.

Cards include the complete recorded attempted/blocked/evidence context,
reversibility, paths, destinations, spend, cost of a wrong answer, and
expiry/default policy. Content outside the specified ASCII/font or byte bounds
returns `422 display_unsupported`; review it in Studio. No clipping, substituted
glyphs, or generated summaries make a request remotely actionable. This rejection
does not change the canonical card or its visibility in Studio. Sample/demo runs
are excluded from the remote view.

The handheld must persist a confirmed command before posting it. Send the same
body bytes and `command_id` when retrying. Mitos checks token authority on every
retry and stores the body digest, original acceptance receipt, and canonical
resolution in **one durable event**. A changed body with the same command ID
returns `409 idempotency_conflict`; a new command must match a pending, current
revision. Console and device answers share the same serialized authority path.

The response is always the original `202` acceptance receipt, even when
application has subsequently completed or become blocked. Read the latest
snapshot for `accepted`, `applied`, or `application_blocked` and application error
text. Replaying a receipt never re-executes the answer. The existing runtime
reconciles accepted operations after a crash, including reuse of an already
created retry child. Receipts remain in the durable log for its lifetime.

Requests are limited to two concurrent operations per device, with a 30-request
burst refilling at two requests per second. Rate limits return `429` with
`Retry-After: 1`. Resolution bodies are limited to 4,095 bytes and reject duplicate,
unknown, missing, or incorrectly cased fields and unsupported versions.

## Connect a waiting agent

Run the wrapper from a terminal or as the command in a Mitos desk session:

```sh
mitos agent codex --workspace /path/to/project --plan
mitos agent codex --workspace /path/to/project --prompt 'Review the release'
mitos agent claude --workspace /path/to/project
```

Both use the installed harness and its existing model configuration. Add
`--url http://127.0.0.1:PORT` for a different Studio port. Arguments after `--`
are passed to the harness. `mitos agent codex-proxy` provides the native stdio
app-server protocol for an existing app-server client; `mitos agent codex`
provides an interactive terminal client. Claude hooks are invocation settings,
not edits to the operator's global configuration. Ordinary unwrapped terminals
do not automatically publish decisions.

The [Codex app-server adapter](https://learn.chatgpt.com/docs/app-server)
accepts `item/tool/requestUserInput` and
`item/commandExecution/requestApproval`. It offers only a one-shot grant and an
offered decline/cancel response, never a session grant or policy amendment.
The [Claude hooks adapter](https://code.claude.com/docs/en/hooks) accepts
`PreToolUse` requests for `AskUserQuestion`, `Bash`, `Write`, and `Edit`.
Claude's plain print mode may disable questions; SDK-style print clients need
their native permission handler configured. Interactive Claude retains its
native question capability.

Source session/thread, native request ID, full request payload, operator,
tenant, and review runs are durably bound at ingress. Identical source retries
reuse the request; changed bodies conflict. Each question in a multi-question
request gets a separate card, and the complete native answer is committed in
the same log event as the last canonical resolution. The adapter can retrieve
that exact response after a Studio restart. It never generates another answer
to recover a failed delivery.

Applied means the source acknowledged receipt: Codex's
`serverRequest/resolved`, or Claude's post-tool/batch hook (including denied
calls). A pipe write alone is not acknowledgement. Delivery failure remains
`application_blocked`; the same response can be retried while the source request
is still live. Ended requests cannot be revived. Wrapper exit, native thread
termination, or expiry of its 30-second heartbeat lease invalidates pending
cards and marks unacknowledged accepted answers blocked.

Supported string schemas use `type: string`, `minLength`, `maxLength`, `pattern`
(Go/RE2 syntax), `enum`, `title`, and `description`. Unknown constraints fail
closed. Answers are at most 1,024 bytes and validated in the canonical authority
path. Choice-only questions preserve native option labels; free-text questions
offer **Send answer** and **Leave unanswered**. The v1 device requires a reviewed
text value with either option; leaving unanswered discards it in the native
response. Studio's card form and `mitos resolve --value 'answer'` also support
these values. Rich, multiselect, explicitly secret/password, and unsupported
protocol requests retain the native console flow. Native tool impact is
explicitly unbounded; the connector does not claim that a command costs nothing
or touches no paths.

## Voice provider

Voice only creates a draft. It never writes a resolution or executes a choice.
Configure a local executable that supplies speech-to-text:

```sh
mitos serve --device-addr 0.0.0.0:8443 \
  --device-cert /path/to/device-chain.pem \
  --device-key /path/to/device-key.pem \
  --device-stt-command /absolute/path/to/transcribe

mitos device pair --name 'Voice handheld' --voice \
  --stt-disclosure 'Local transcription on this host; no audio retention.'
```

The executable receives raw signed 16-bit little-endian mono PCM at 16 kHz on
stdin and writes only the transcript (at most 1,024 bytes) on stdout. It inherits
server configuration and receives `MITOS_STT_SAMPLE_RATE=16000`,
`MITOS_STT_CHANNELS=1`, `MITOS_STT_FORMAT=s16le`, and `MITOS_STT_LOCALE=en`.
Mitos invokes it directly, without a shell, discards stderr, and cancels it at
the provider deadline. The Go `device.Transcriber` interface also permits embedded
providers. No transcription engine or model is bundled.

Alternatively, use the built-in multipart HTTP provider with the
[audio transcription API](https://developers.openai.com/api/reference/resources/audio/subresources/transcriptions/methods/create)
or a compatible local server:

```sh
mitos serve --device-addr 0.0.0.0:8443 \
  --device-cert /path/to/device-chain.pem \
  --device-key /path/to/device-key.pem \
  --device-stt-url http://127.0.0.1:8080/v1/audio/transcriptions \
  --device-stt-model whisper-1
```

For an authenticated service, set `MITOS_STT_API_KEY` in the server environment.
Remote endpoints require HTTPS; HTTP is accepted only for loopback. Redirects
are rejected. Configure either the HTTP provider or the executable, not both.

Use the pairing disclosure to describe the actual provider, including external
routing and any provider retention. Provider credentials stay on the server.
Without a configured provider the endpoint returns `503 stt_unavailable`.

Uploads must be canonical 44-byte-header PCM WAV, 0.2–12 seconds, no extra chunks
or samples, and at most 384,044 bytes. Mitos checks authority and card revision
before reading audio and again before STT and before returning its result. The
upload and provider share an 18-second deadline, with a 19-second upload read
deadline and at most two outstanding transcriptions.
Mitos does not persist audio or unconfirmed transcripts and clears the owned
audio buffer after processing. Only an exact offered option ID or label proposes
a `choice`; other speech leaves it empty for explicit selection. For text cards,
the transcript is the proposed `value`, checked against the canonical schema,
and `choice` remains empty for explicit selection. Silence, unsupported text,
and schema violations return `422`. For choice-only cards, `value` is null.

## Validation and hardware acceptance

The Go backend is implemented. Hardware provisioning, microphone capture,
display, and reconnect-journal acceptance belong to esp-notify and have not
been exercised on a physical board here. Actual speech recognition accuracy
depends on the configured provider and microphone; protocol tests use controlled
transcripts rather than a bundled speech model.

Backend tests cover the device lifecycle, verified TLS and name/chain rejection,
wrong-tenant and permission denial, strict wire parsing, bounded pagination,
revocation during STT, voice-only drafts, rate limits, console/device races,
atomic receipt recording, failed persistence, and restart recovery before and
after retry child creation. Tests also cover text schemas, voice drafts requiring
explicit confirmation, source deduplication, multi-question responses, failed
delivery/restart, session termination, and a dropped TLS connection after commit.

The installed Codex app-server and Claude CLI passed real native question and
approval round trips against deterministic local inference. These opt-in tests
exercise the harness processes and Claude hook executable without paid model
calls or execution of the denied command:

```sh
go test -race ./...
go vet ./...
go build -o /tmp/mitos-spec17 ./cmd/mitos
MITOS_LIVE_CODEX=1 go test ./internal/web -run TestLiveCodexNative -v
MITOS_LIVE_CLAUDE=1 MITOS_TEST_BIN=/tmp/mitos-spec17 \
  go test ./internal/web -run TestLiveClaudeNative -v
```


The handheld's default **From phone** flow now uses its own temporary WPA2 access
point and local portal. Its `/api/status` and `/api/join` exist only on that watch
hotspot, not on this connector. Mitos maintains the embedded phone assets but
never receives Wi-Fi credentials. See [Wi-Fi setup](19_HANDHELD_WIFI_SETUP.md).
