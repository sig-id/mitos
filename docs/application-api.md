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
It needs no external database service, broker or remote worker.

## Not provided

This is the S1 slice only. Service-principal authentication, tenant isolation,
Remote worker claims/leases/checkpoints, MQTT work
ownership and checkpointed conversation waits (gates S2–S4) are not implemented;
the API remains loopback-only and same-user access is not multi-tenant
authorization.

## Runtime storage and crash fixtures

The runtime uses SQLite (`runtime.db`) with WAL and `synchronous=FULL`. Canonical
Event records, their hash chain and rebuildable projections commit together. The
one-time importer verifies `events.jsonl` and any legacy agent library, keeps the
originals unchanged, and records completion transactionally. Later starts never
read or write those snapshots. Unknown/newer schemas refuse startup. A failed or
ambiguous commit latches the writer unavailable until reopen/replay discovers the
durable tail; it cannot publish or perform another mutation against an uncertain head.

The journal interface exposes replay, event/projection append and close. Artifact
bytes use a separate store, with metadata published only after durable byte storage.
Exact library revisions are journaled. Offline audits read a consistent read-only
SQLite snapshot and prefer the database over its same-directory JSONL backup;
historical JSONL audit inputs remain supported without repair.

The local operator can `POST /api/storage/backup` with `{"directory":"/new/path"}`
to create an online backup. `mitos storage restore --from BACKUP --data NEW_DIR`
verifies a staging copy and advances its authority epoch before publication.
Existing destinations are refused. Backups include the database, registered
artifacts, the MQTT signing key and stored credential files; original workspaces, environment secrets
and native CLI conversation stores remain separate. `POST
/api/storage/expire-receipts` with a past RFC3339 `before` timestamp expires terminal
structured request keys; tombstones reject retries with `410 request_key_expired`.
The corresponding CLI is `mitos storage expire-receipts --before TIMESTAMP`.

Crash fixtures explicitly set `MITOS_ENABLE_FAILPOINTS=1` and `MITOS_FAILPOINT`
to an exact boundary: `journal.before_append`, `journal.after_append`,
`sqlite.before_commit`, `sqlite.after_commit`, or `artifacts.after_put`. The process
exits with code 86 without cleanup. SQLite commit points test transaction rollback
and durable recovery of complete event/projection batches. Leave these disabled
outside fixture subprocesses.
