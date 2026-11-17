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
  `description`, `title`. The root is an object; every object sets
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
- **Reads:** `GET /api/runs/{id}` (status), `GET /api/runs/{id}/events`,
  `GET /api/runs/{id}/result` (`409` until an accepted result exists), and
  `POST /api/runs/{id}/cancel`.

Structured work is not project-scoped and uses the `advisory` enforcement policy.
It needs no database, broker or remote worker.

## Not provided

This is the S1 slice only. Service-principal authentication, tenant isolation,
PostgreSQL persistence, remote worker claims/leases/checkpoints, MQTT work
ownership and checkpointed conversation waits (gates S2–S4) are not implemented;
the API remains loopback-only and same-user access is not multi-tenant
authorization.
