# Custom service events

Named custom events deliver JSON data to active event subscribers through the
same durable queue as [service outcome events](SERVICE_EVENTS.md). Configure a
normal service work binding and an event subscription:

```json
{"id":"summarize","scope":"project","trigger":"event",
 "schedule":{"type":"event","trigger":["inventory.ready","report.*"]}}
```

Names start with a lowercase letter and contain at most 96 lowercase letters,
digits, dots, underscores, colons, or hyphens. Exact names, one trailing wildcard,
and arrays are supported; `*` matches all supported event names. Overlapping
patterns allocate only one delivery per subscriber. Names beginning with
`service.`, `worker.`, `task.`, `run.`, or `decision.`, and the exact name
`scheduling_event`, are reserved. Callers cannot emit authoritative system
outcomes. `service_trigger_event` is an ordinary supported custom name.

## Emit and inspect

```sh
mitos emit-event --key inventory-42 --file inventory.json inventory.ready
mitos service-events
mitos service-events 42
```

The last command reads event sequence 42. Use `--url` for a nondefault loopback
port. The payload file contains one JSON value. Event names and keys identify
emissions independently of the target services; emission has no `--cycle` or
`--source` option.

```http
POST /api/service-events
Content-Type: application/json

{"name":"inventory.ready","idempotency_key":"inventory-42",
 "payload":{"items":12}}
```

`GET /api/service-events` lists the latest 50 custom events, newest first, with
identity, timestamp, payload digest/size, and delivery counts. It omits payloads.
`GET /api/service-events/{seq}` returns one event with its payload and original
recipient list, including current delivery status and any Run IDs. The Services
page provides an emission form, recent receipts, and links to subscriber Runs.
HTML shows metadata and links to the JSON payload rather than rendering payloads.

The whole HTTP JSON envelope is limited to 64 KiB. Payloads use the same bounded,
number-preserving [JSON normalization](SERVICE_INGRESS.md#payload-and-identity)
as webhook intake. Missing payloads, duplicate object keys, unknown envelope
fields, and reserved names are refused. `null` is a valid payload. The HTML form
retains Studio's 32 KiB encoded-form limit; use the API or CLI for larger input.

New and duplicate emissions return HTTP 200 with the source receipt. Changed
content under a saved name/key returns 409; invalid input returns 400, unsupported
media 415, excessive body size 413, and a missing receipt 404.

## Durability and execution

Emission saves one global `effect/custom_event_emitted` record containing the
name, key, normalized payload, digest/size, catalog revision, and initial recipient
allocations. It does not create a source Task or claim an application success.
It persists even when no active subscriber matches. Subscriber Task creation and
delivery consumption subsequently occur in one synced Effect per recipient.

The name and idempotency key are reserved for the lifetime of the log. Repeating
them with identical normalized content returns the original source sequence,
hash, timestamp, and current delivery progress. Whitespace and object-key order
do not change identity; numeric lexical changes do. A duplicate never rematches
subscriptions, restores skipped deliveries, or consumes another budget slot.
Restart rebuilds this identity and pending allocations from the verified log.

Every direct subscriber receives `service_event` on worker stdin and in its Task.
The cause includes the source Effect sequence/hash, root sequence/hash, depth 1,
one event name, and a `custom` object with the saved name, idempotency key, payload,
SHA-256, and byte count. Source service/Task/Run IDs are empty. Installed work
bindings still select the command, workspace, and acceptance policy. Payloads
are durable local application input, not a mechanism to replace those bindings.

Direct subscribers share a completion-dependency cycle identified by the source
Effect and allocation revision. A `depends_on` prerequisite must be a successful
member of that same subscriber cohort. Missing or skipped prerequisites do not
count as success, and the existing bounded wait applies. Retries preserve the
original Task cause and cycle. See [service dependencies](SERVICE_DEPENDENCIES.md).

Final subscriber outcomes emit the registered service outcome names normally.
Their descendants carry that immediate Task outcome as their cause, retaining
the custom root's sequence/hash and advancing depth. The original custom payload
is only supplied directly to the custom event's subscribers; it is not implicitly
copied down the entire graph. Explicit data dependencies handle artifact transfer.

The shared limits are eight delivery generations and 64 allocations across the
whole root, including the initial custom subscribers. Pending allocations reserve
capacity and spend the root budget even if later suppressed or rejected. Breakers,
abort, replacement, scheduling, conditions, health, claims, and acceptance retain
their existing behavior. Subscription/work changes can suppress pending delivery;
new subscriptions never receive historical events. A recorded emission or submitted
Task is not evidence of successful execution.

## Boundary

Emission is a local API/CLI/UI operation protected by Studio's existing loopback
and cross-origin rules. Custom names are caller claims, not authenticated external
producer identity. There is no generic worker-controlled Effect injection, raw
`worker.completed` or `scheduling_event` producer integration, transport connector,
public event bus, or distributed delivery fencing. The local provider still grants
zero unattended execution; the full product gates remain unmet.
