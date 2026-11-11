# Human decisions and local resolution

Status: Normative local behavior and API contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

The console, CLI, and JSON API use the same stored card and resolution operation.
Requests now bind their run, tenant, causal request Decision, and server timestamp.
The card contains structured `context` (`attempted`, `blocked_by`, and artifact
references), options with consequences, a blast-radius declaration, and structured
cost of a wrong choice. Context is assembled from the brief, failure signal, and
saved artifacts. It does not copy a command transcript or an agent rationale.

## Policy and silence

`internal/decision` classifies an act from substrate-supplied workspace confinement,
checkpoint availability, exact destination grants, compensating actions, spend,
and missing-input/judgment flags. Confined checkpoint-reversible acts classify as
`internal` (log only); granted compensatable consequences classify as `recoverable`
(digest); unknown or externally visible consequences classify as `external` (park).
A destination that requires approval remains external even if it is compensatable.
Path escape, missing confinement, missing recovery, and ungranted destinations
cannot earn internal permission.

The current same-user command provider cannot enforce confinement. Its recovery,
observation, and oracle cards therefore remain external. The deterministic
assessment is recorded with the request. Supplying a lower reversibility class in
an application or card cannot change those provider facts.

The card schema represents `default`, `ttl_ms`, and `value_schema` explicitly as
nullable fields. It rejects external defaults, unmatched default/TTL pairs,
unavailable default options, invalid costs, duplicate options, multiple recommended
choices, and internal decision cards. Recoverable Studio cards require a default
and TTL; Market cards forbid defaults. TTL calculation uses the recorded request
time and has a defined expiry boundary.

**Automatic default execution is not connected to this local provider.** It does
not issue recoverable cards, digest actions, or unattended grants. All currently
issued cards explicitly hold until answered. Scheduler-driven `decision_defaulted`,
provider-backed hold/park transitions, and digest transport remain outstanding.

## Durable request and resolution

A request Decision saves the complete card and parks execution before the
`approval_requested` Effect publishes it. A crash between those records leaves a
pending request. Startup reconstructs the same card, ID, and timestamp before any
command dispatch. Repeated identical requests for one run retain its pending card.
Cross-worker/tenant card grouping is not yet implemented.

A resolution records `decision_id`, `resolver`, `choice`, optional `note`,
`resolved_at`, and `via` in `approval_resolved`. The server supplies identity and
time. The connector accepts only the offered choice and supported input shape.
It validates checkpoint scope, memo, target, original deadline, and restart
intensity before accepting a rewind, then checks execution state again when
applying it.

The first valid resolution wins. Later duplicates and conflicting choices produce
`decision_rejected` evidence and cannot replace the first resolver, note, or choice.
The same-choice response returns the original result. Invalid or out-of-tenant
principals are rejected before changing execution. Rejected input values and notes
are not copied into rejection events.

`decision_applied` binds an accepted choice to its result run. Retry application
recovers across a crash before child creation and after child creation before the
Effect, reusing the one child attempt. Rewind application delegates to the existing
checkpoint mechanism. Accepted but temporarily unexecutable choices remain
recorded; `decision_application_blocked` names the failure without repeating the
same event every dispatch tick. An operator stop supersedes an unapplied retry.
Expired recovery becomes a new explicit choice to start a fresh attempt or close.

## Local identity boundary

New runs are stamped with `{ "org": "local", "project": "studio" }` at creation.
The loopback server supplies `local-operator` to console/API resolutions. The CLI
uses that same boundary; its transport label is informational, not authentication.
JSON clients cannot provide a resolver, tenant, resolution timestamp, or a widened
blast radius. Remote connectors must supply an authenticated principal and cannot
reuse the local assertion.

This remains a trusted, single-operator deployment. A process able to access the
local server acts as that operator. Sigid assertions, hosted tenant-filtered reads,
buyer/seller authorization, and remote account binding remain required before a
multi-tenant or remote service can be offered. Historical unstamped local records
remain readable without rewriting their original log bytes; they do not acquire
hosted tenant provenance.

## CLI and API

```sh
mitos decisions
mitos resolve --choice cancel --note 'Keep the evidence.' local_RUN_ID decision_ID
mitos resolve --choice retry local_RUN_ID decision_ID
```

Use `--url` before the IDs for another local server. A rewind with a custom memo
continues to use `mitos rewind`; it now records the same resolver provenance as
`resolve`. The console's checkpoint option supplies its deterministic failure memo.

| Method | Path | Result |
|---|---|---|
| GET | `/api/decisions` | Pending unanswered cards |
| GET | `/api/runs/{id}/decision` | Current card and its resolution/application state |
| POST | `/api/runs/{id}/decisions/{decision}` | Record `choice` and optional `note` |

A POST accepts exactly one JSON object, for example
`{"choice":"retry","note":"The missing dependency is now installed."}`.
Unknown fields and trailing JSON return 400. Unavailable choices or stale state
return 409. Success returns the result `run_id` and `decision_id`. A retry result
points to the child run; close and rewind refer to the original run.

The runtime currently executes choice cards for retry, close, and checkpoint
recovery. Generic value entry/JSON-Schema validation, brief amendments, remote
notifications and resolutions, delivery receipts, cross-worker dedupe, and scheduled
digests remain required by `15` and `10`. Canonical Decision/Effect envelopes and
model-selected orchestration remain separate outstanding control-plane work.
