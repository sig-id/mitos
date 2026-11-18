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

Remote worker claims/leases/checkpoints, MQTT work
ownership and checkpointed conversation waits (gates S2–S4) are not implemented;
the operator API remains loopback-only. Authenticated application access uses the
separate TLS listener described below.

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


## Service principals and the application listener

`serve --application-addr HOST:PORT --application-cert CERT --application-key KEY`
serves a separate TLS-only API. The ordinary operator listener stays on loopback.
Create scoped credentials through local `POST /api/service-principals` with `id`,
`name`, `tenant: {org, project}`, `read: true`, optional `write: true`, permitted
`agents` (default `mitos.generalist`), and future `expires_at` (at most 366 days).
The response includes a one-time opaque token. Lists omit its hash; plaintext is
never journaled. Rotate with `POST /api/service-principals/{id}/rotate` and
`expires_at`, or revoke with `DELETE /api/service-principals/{id}`. The `principal`
CLI wraps these operator operations.

Every application request sends `Authorization: Bearer TOKEN` and the API version
header over TLS. Its request-scoped principal produces a `Store.As(principal)`
view; handlers receive that view through an application session, never the global
store. The view revalidates permissions, expiry, credential revision and authority
epoch on each operation. Cross-tenant identifiers return 404 for tasks, runs,
artifacts, events, results and cancellation. Tenant identity is never taken from a
submitted brief or a URL parameter. Operator, configuration, project, graph and
storage routes are not installed on this listener.

Application operations are `POST /api/tasks`, `GET /api/tasks/{id}`, `GET
/api/runs`, `GET /api/runs/{id}`, `GET /api/runs/{id}/events`, `GET
/api/runs/{id}/result`, `GET /api/runs/{id}/artifacts/{name}`, and `POST
/api/runs/{id}/cancel`. Structured admission uses an index keyed by tenant and
request key. The receipt binds the original principal and request digest: an
identical retry returns its original Run; changing either conflicts. Expired keys
retain tenant-scoped tombstones. Current principal permissions are checked before
local execution and before acceptance.

S2 application-boundary conformance is demonstrated by the independent application
fixture and its four runtime/application crash cases. `service_principal_auth` and
`tenant_isolation` are true; PostgreSQL, remote workers and dormant waits retain
their separate gates.


## Bounded reads, capacity and restart recovery

`GET /api/runs`, per-run events, tenant `GET /api/events`, and `GET /api/receipts`
accept `after` (nonnegative journal sequence) and `limit` (1..200, default 50).
They return arrays bounded by item count and 16 MiB. Run/detail replies omit inline
event histories; use the event endpoint. `Mitos-Next-After` and `Mitos-Has-More`
carry pagination state. Run lists also return `Mitos-Next-Run`; send it as
`after_id` when several graph runs share a creation event. Application receipts
contain admission/outcome identity and accepted result digests; filtered empty
pages still advance the cursor. Tenant feed indexes rebuild from canonical replay.
The local CLI accepts `--cursor`, `--after-run` and `--limit` on list/log reads.

The local operator configures `GET/POST /api/runtime-policy`: `capacity` (1..128),
`default_tenant` and optional `tenants` overrides keyed by `org/project`. Tenant
limits are `concurrency`, `queue`, `attempts_per_task`,
`elapsed_minutes_per_task`, and `weight` (1..16). Queue overflow returns 429; budget
refusal returns 422. Weighted unit-cost deficit round-robin preserves pool ordering
within each tenant; its consumed credit commits with admission. Existing pool
configuration still applies within global capacity. Task attempt/time limits freeze
at admission and include waiting time. These are execution/attempt limits, not
claims of native model-token or monetary metering.

Interrupted effect-free structured work automatically receives a bounded new Run
under its original Task, request key, frozen definition and remaining budget.
Workspace/PTY work retains inspection-based recovery. Cancellation never triggers
a replacement. Exact definition bodies are in the admission journal and backups;
recovery never silently selects the current bundled revision.

Tenant application execution requires `MITOS_AGENT_CONFINEMENT=bwrap` and
`MITOS_AGENT_NETWORK=unrestricted`. Workers then run in an isolated network
namespace whose only egress is a mediated model transport: an allowlisted
CONNECT relay on a UNIX socket admitting just the model API endpoints, reached
through a loopback forwarder inside the sandbox (`HTTPS_PROXY`). Host services —
including the loopback operator listener — are unreachable from the sandbox, and
the conformance stand-in proves both the mount and network boundaries. Workers
use a fresh native home and dedicated `OPENAI_API_KEY`; operator OAuth stores,
native history, MCP configuration and unrelated credentials are not shared.
Missing prerequisites fail visibly. Local operator S1 work retains its existing
native-login profile and its unmediated network posture.

`examples/appfixture` is a separate Go module and database using HTTP only. It
atomically saves message/dispatch intent, admits one Task, reads bounded event
feeds and accepted results, and deduplicates one durable domain reply. The test
kills both application and runtime at before/after admission and before/after
publication. Run it with `go test -race ./internal/application -run
TestIndependentApplicationCrashMatrix`; it uses a deterministic model stand-in
through the real confined worker path, not a production-model benchmark.

The same TLS listener also hosts the independently authenticated and versioned
[structured worker protocol](worker-protocol.md). Application credentials have
no access to its claims or context.
