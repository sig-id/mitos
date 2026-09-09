# Webhook and external-signal services

Webhook and external-signal deliveries create ordinary durable Tasks through the
configured service work binding. They use the existing pools, concurrency limits,
replacement, breakers, abort/retry policy, conditions, health checks, ownership,
and acceptance. Incoming JSON is data for the installed application; it cannot
select another application, workspace, command, or acceptance policy.

## Webhooks

Configure a service unit with an explicit trigger and path:

```json
{"id":"resize","scope":"project","trigger":"webhook",
 "schedule":{"type":"webhook","path":"/capabilities/resize/runs"}}
```

Give it the usual work binding in the project catalog, then POST one JSON value
to that path on the running Studio:

```sh
curl http://127.0.0.1:4646/capabilities/resize/runs \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: delivery-42' \
  --data '{"image":"example.png","width":640}'
```

Exactly one `Idempotency-Key` header supplies the external delivery ID. The optional
`X-Mitos-Cycle` header supplies a shared completion-dependency cycle. Query strings,
encoded-path aliases, and repeated identity headers are refused. Webhook paths
must be canonical absolute paths of 2–256 ASCII bytes, using letters, digits,
slash, dash, dot, underscore, and tilde. Studio's reserved top-level routes cannot
be used, and two active services cannot own the same path. There are no wildcard
routes. Removing or disabling a route stops new deliveries to it.

## External signals

A named external source uses the explicit signal endpoint:

```json
{"id":"jobs","scope":"project","trigger":"external_signal",
 "schedule":{"type":"external_signal","source":"queue.jobs"}}
```

```http
POST /api/services/jobs/signals
Content-Type: application/json

{"source":"queue.jobs","delivery_id":"message-42",
 "payload":{"job":"example"},"cycle":"batch-7"}
```

`cycle` is optional unless the service declares completion dependencies. The source
must match the configured source name. Unknown envelope fields and missing payload
are refused. A payload can be any supported JSON value, including `null`.

The CLI uses a JSON payload file:

```sh
mitos service-signal --source queue.jobs --key message-42 \
  --file message.json --cycle batch-7 jobs
```

Use `--url` for a Studio on a nondefault loopback port. Source names use bounded
service identifiers. Delivery and cycle keys use the existing nonempty, at-most
128-byte service-key rules. The caller supplies stable delivery IDs; Studio does
not generate replacement IDs when the sender retries.

## Payload and identity

HTTP deliveries require `application/json` and at most 64 KiB for the whole body.
For signals, that includes the envelope. Normalized payloads also have a 64 KiB
limit, maximum nesting depth 32, and maximum 10,000 values. Invalid UTF-8, duplicate
object keys, multiple top-level values, and malformed JSON are rejected.

Normalization sorts object keys and removes insignificant whitespace while
preserving JSON numeric tokens, including integers beyond floating-point precision.
Object ordering and whitespace changes therefore preserve identity; changing a
number's lexical representation (for example, `1` to `1.0`) changes identity.
SHA-256 and byte count refer to the normalized payload, not the wire formatting.

Task creation and provenance are one synced Effect. `service_ingress` records the
kind, configured source/path, delivery ID, optional cycle key, server receipt time,
normalized payload, digest, and size. It is saved with `ServiceCall` and the Task.
The worker receives that object in stdin alongside its Brief and dependency
metadata. Run and Services pages show delivery/source metadata; Task JSON and
worker input retain the payload. Payloads are durable local input, so send only
content intended for the installed application and the Studio event log.

A webhook delivery key is scoped to its route path, including across reassignment
to a different service. An external-signal delivery key is scoped to its service
and trigger kind. Repeating a saved delivery with identical source, cycle key,
and normalized content returns the original Run receipt, even after completion,
configuration removal, or server restart. It does not create another Task, refresh
the receipt timestamp, or append another acceptance event. Reusing the key with
different content/source/cycle is a conflict. Original receipts remain readable
when their route is disabled; a new key requires a currently active binding.

Admission refusal, invalid payload, missing application/workspace, capacity limits,
source mismatch, and stale catalog validation do not consume a delivery key.
Retry the same delivery after fixing the reason. Concurrent duplicate requests
serialize through the existing intake action and Store writer. Recovery rebuilds
receipt indexes from the verified event chain, not a separate delivery cache.

## Execution and responses

Success returns HTTP 200 with the original Run receipt for both new and duplicate
intake. This acknowledges durable enqueueing, not completed execution. Poll the
existing Run/Task APIs or CLI to observe completion. Malformed requests return 400,
unsupported media types 415, oversized bodies 413, unavailable webhook paths 404,
and conflicting or currently refused admission 409. Cross-origin and loopback-only
protections remain in force.

Completion dependencies share an explicit `ingress:CYCLE_KEY` namespace across
webhook and signal receipts in the same catalog revision. Send the same key to
all prerequisite members; no prerequisite is automatically triggered. Without an
explicit cycle, independent deliveries get standalone cycles. A dependent service
refuses a delivery without a shared cycle key. See
[completion dependencies](SERVICE_DEPENDENCIES.md) for exact-success witnesses,
revision boundaries, and bounded waits.

Retries preserve the Task's original payload, provenance, and cycle. Conditions
can skip a delivered attempt without losing its receipt. Health/retry policies and
replacement use their existing stop/harvest paths. Result acceptance remains
independent of delivery validity: successfully parsing or persisting a message is
not a successful application result.

## Current boundary

These endpoints are local ingress adapters. Studio still binds to loopback and
checks local Host and cross-origin mutation rules. Source names and delivery IDs
are caller claims matched to configuration, not authenticated third-party identity.
There is no public webhook hosting, signature verification, secret management,
queue subscription/polling client, or upstream acknowledgement adapter here. A
trusted local bridge can normalize an external transport into these endpoints;
provider-specific connectors remain a separate integration requirement.

[Custom service events](SERVICE_CUSTOM_EVENTS.md) use separate emission and
subscription APIs. Lifecycle propagation, readiness gates,
distributed delivery coordination, hosted tenancy, and the full product gates
remain outstanding. Incoming content does not raise the local provider's zero
unattended grant or establish oracle isolation.
