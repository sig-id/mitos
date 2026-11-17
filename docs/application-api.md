# Application API and headless structured work

## API versioning

Every request to `/api/...` must state the API version it speaks:

```
Mitos-API-Version: 1
```

WebSocket upgrades (for example `/api/sessions/{id}/stream`) carry the version as
the subprotocol `mitos-api.v1`, because browsers cannot set custom headers on
WebSocket requests. A request without a version, or with a version this server
does not implement, is refused with `400` and
`{"error":"unsupported_api_version","supported_versions":["1"]}`. Responses echo
the negotiated version in the same header. There is no unversioned fallback.
The browser pages, the `mitos` CLI and the agent bridge all send the header.
Links in pages that point at `/api` are fetched with the header and saved as
downloads, because plain navigation cannot carry it. The Claude and Pi attention
adapters and the `tools/terminal-eval` scripts send it as well.
Pages, static assets, webhooks and the separate HTTPS device API
(`/api/device/v1/...`, path-versioned on its own listener) are outside this check.

`GET /api/capabilities` lists supported versions, the application operations, the
structured-work profile, per-runtime agent capability profiles and the hosted
features this build does **not** provide.

## Headless structured work (delivery gate S1)

Structured work runs an agent definition on structured JSON input with no
repository, workspace, Git history or PTY, and accepts only an exact typed result.

Admit with `POST /api/tasks` (or `/api/runs`):

```json
{
  "title": "Classify the ticket",
  "intent": "Return the category and a one-line reason.",
  "app_id": "agent:mitos.generalist",
  "timeout_minutes": 10,
  "structured": {
    "key": "ticket-1234-classify",
    "input": {"ticket": "…"},
    "result_schema": {
      "type": "object",
      "additionalProperties": false,
      "required": ["category", "reason"],
      "properties": {
        "category": {"type": "string", "enum": ["bug", "feature", "question"]},
        "reason": {"type": "string", "maxLength": 200}
      }
    }
  }
}
```

- **Idempotency:** `structured.key` identifies the request. Replaying the same
  request returns the original run; reusing the key for a different request
  returns `409 request_key_conflict`.
- **Binding:** admission freezes the exact definition revision and digest; later
  library edits cannot change admitted work.
- **Input:** a JSON object up to 256 KiB, presented to the agent as data.
- **Result schema:** the strict structured-output subset (up to 64 KiB, depth 16):
  `type`, `properties`, `required`, `additionalProperties`, `items`, `enum`,
  `minLength`, `maxLength`, `minimum`, `maximum`, `minItems`, `maxItems`,
  `description`, `title`. Numeric bounds are compared exactly (no float rounding);
  numbers are limited to 128 characters and exponents to ±400. The root is an object; every object sets
  `additionalProperties: false` and requires all of its properties. Other keywords
  are refused at admission.
- **Execution:** Codex runs read-only in an empty scratch directory with the schema
  as its output schema, no Mitos handoff tools and no network. The authority
  receipt uses the `codex-headless` profile, so definitions that require workspace
  access (for example `mitos.engineer`) are refused at admission.
- **Acceptance:** the final answer must be exactly one JSON value that validates
  against the schema. It is saved canonically as `result.json` and the run
  finishes verified. Anything else fails the attempt with
  `result_schema_mismatch`; the rejected text is kept as `rejected-result.txt`.
- **Retry:** retrying a failed or interrupted run (including the restart
  recovery decision) creates a child attempt that reuses the frozen request and
  definition binding exactly; it is never re-admitted against the current library.
- **Reads:** `GET /api/runs/{id}` (status), `GET /api/runs/{id}/events`,
  `GET /api/runs/{id}/result` (`409` until an accepted result exists), and
  `POST /api/runs/{id}/cancel`.

Structured work is not project-scoped and uses the `advisory` enforcement policy.
It needs no database, broker or remote worker.

## Not provided

This is the S1 slice only. Service-principal authentication, tenant isolation,
SQLite transactional persistence, remote worker claims/leases/checkpoints, MQTT work
ownership and checkpointed conversation waits (gates S2–S4) are not implemented;
the API remains loopback-only and same-user access is not multi-tenant
authorization.

## Storage preparation and crash fixtures

The runtime Store uses a journal interface for ordered replay, durable event and
projection append, and close. Task/Run state still comes from the existing Event
records and `apply`; indexes do not define separate lifecycle semantics. Retained
artifact bytes use a separate store interface and become accessible only after
their journal metadata commits.

The current JSONL adapter retains its historical complete-record-prefix recovery.
It rejects projection updates because it cannot commit them transactionally. The
SQLite replacement is the next storage slice; it will run the same transactional
journal conformance harness as the in-memory test fake and remove the JSONL
runtime path after a verified one-time import. This preparation does not change
runtime capabilities or claim SQLite, tenant isolation, or remote-worker support.

Crash fixtures explicitly set both `MITOS_ENABLE_FAILPOINTS=1` and
`MITOS_FAILPOINT` to one of `journal.before_append`, `journal.after_append` or
`artifacts.after_put`. The selected boundary exits with code 86 without shutdown
cleanup. The append points bracket the adapter durability barrier and precede
in-memory publication; the artifact point follows durable byte storage but
precedes metadata commit. Leave failpoints disabled outside test subprocesses.
The SQLite adapter must add injection inside its transaction to demonstrate
all-or-none crash recovery, beyond these Store-level boundaries.
