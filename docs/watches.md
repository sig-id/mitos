# Watches

A watch tells you when runtime events in a project, graph, task or run need your
attention. The runtime evaluates it over the journal; nothing runs in a workspace.
The contract is [spec 09](../spec/system/09_WATCHES_AND_TRIAGE.md). This release
implements gates W1–W4: watches owned by the operator, by an adaptive graph's
responsibility, or by one attempt, with optional decision-model triage, delivery
outcomes and the calibration report.

## Create one

```json
{
  "version": "mitos.watch/v1",
  "owner": {"kind": "operator"},
  "description": "payments graph failures",
  "condition": {"scope": {"graph": "graph_…"}, "types": ["effect"], "kinds": ["verification_failed"]},
  "lease": {"expires_at": "2026-11-21T09:00:00Z", "max_renewals": 16},
  "grouping": {"by": ["run_id"], "wait_ms": 2000, "interval_ms": 60000, "max_events": 32, "repeat_interval_ms": 3600000},
  "max_silence_ms": 900000,
  "default_level": "notify"
}
```

```sh
mitos watch create --file watch.json
mitos watch list
mitos watch deliveries --id watch_…
mitos watch renew --id watch_… --expires 2026-11-21T21:00:00Z
mitos watch cancel --id watch_…
mitos watch items --id watch_…
mitos watch resolve --item witem_…
mitos watch inbox --owner responsibility:graph_…
mitos watch outcome --delivery delivery_… --outcome acted
mitos watch outcome --delivery delivery_… --mark needless_wake
mitos watch report --id watch_…
```

The HTTP API is under `/api/watches` and needs the `Mitos-API-Version` header.
Unknown fields are refused.

## What you receive

- **Matching events** are grouped by the `grouping.by` fields. A group is
  delivered `wait_ms` after its first event, then at most once per `interval_ms`
  while events continue, or immediately when it reaches `max_events`.
- **Must-deliver events** reach every watch whose scope contains them, even
  when its `kinds` filter does not match: finished, failed, cancelled and parked
  runs; quiescence `block` edges; `blocked_on_human`; an agent question that
  blocks an attempt on a human answer (`agent_awaiting_answer`, wake), and its
  answer or end (`agent_answered` / `agent_request_ended`, notify);
  `observation_failed`; supervision incidents; parked model decisions. Rules at
  `wake` or `escalate` are delivered immediately.
- **Levels.** `digest` and `notify` deliveries are kept in the watch's delivery
  feed. `wake` and `escalate` deliveries appear in **Notifications**. Further
  escalations for the same watch and group update the open item until you
  resolve it or `repeat_interval_ms` passes.
- **Overlap.** If several of your watches match the same event, you receive it
  once, at the highest level any of them assigns.
- **Lease and silence.** A watch ends at `lease.expires_at` with a notice of how
  many events were still waiting. With `max_silence_ms`, you are told once when no
  matching event arrives for that long.

Deliveries are journal receipts. A restart between an event and its delivery
neither loses nor repeats it. A receipt never changes. Its `route` is `feed`,
`inbox` or `folded`, and `item_id` names the Notifications item it opened or
updated. Item state lives on the item (`mitos watch items`), and inbox state on
the owner's inbox entry (`mitos watch inbox`).

## Watches for orchestrators and attempts

`owner` can also be `{"kind": "responsibility", "id": "graph_…"}`, which is an
adaptive graph's supervisory responsibility, or `{"kind": "attempt", "id": "run_…"}`,
which is one attempt in a graph. Both may watch only their own graph. An unset
project or graph in the scope is filled in, and a different one is refused.

- **Wake.** A responsibility has no running session between planning rounds. A
  `wake` or `escalate` delivery registers a graph trigger in the same journal
  record, and the kernel starts the next planning round from it. The round's
  context lists the pending deliveries. A restart or an orchestrator replacement
  neither loses nor repeats the wake.
- **Inbox.** Up to 64 deliveries queue per owner. `digest` deliveries, and
  `notify` deliveries once the inbox is full, are folded into a digest. A full
  inbox sends one `watch_degraded` notice. Wakes still queue and still wake. The
  next queued delivery carries the digest.
- **Generation.** Each delivery records the orchestrator generation current when
  it was written. Only the current generation can record an outcome. A replaced
  orchestrator's outcome is refused.
- **Endings.** An attempt's watches end when the attempt finishes. Closing the
  graph revokes its watches with a notice.

Agents use the `watch_create`, `watch_renew`, `watch_cancel`, `watch_inbox` and
`delivery_outcome` tools. They get these tools only when granted the `watch.manage`
capability; the default orchestrator definition requests it. Deliveries arrive as
`mitos.cooperation/v2` `observation` messages marked as untrusted evidence.

## Triage

Add a `triage` block to let a decision model choose the level for each group:

```json
"triage": {"instructions": "Wake only when the failure changes which task should run next.", "max_calls": 32}
```

- **Provider.** Triage uses the decision provider the owner already has. For a graph
  owner, that is the graph's `decision_application`, or else the project policy's
  provider. Operator watches use the project policy's provider. The provider's
  `min_confidence` and `min_probability` are the thresholds, and its timeout is
  capped at 30 seconds.
- **Allowance.** Set `limits.triage_calls` on the graph, or `triage_calls` in the
  project agent policy for operator watches. Saving the policy refills it.
  `max_calls` narrows one watch's share for its whole life. When an allowance runs
  out, you get one `watch_degraded` notice, and groups keep arriving at the default
  level.
- **Levels.** The model may always raise the level. It may lower it below
  `default_level` only when both thresholds are met. `escalate` is offered only with
  `"allow_escalate": true`.
- **Failures.** Every failure delivers at `default_level`. That covers a refusal, a
  timeout, an invalid answer, a missing provider or credential, and a request still
  pending at restart (`outcome_unknown`).
- **Audit.** Each delivery records its `source` (`rule`, `default`, `triage` or
  `fallback`) and its `fallback` reason. `mitos watch triage --id WATCH_ID` (or
  `GET /api/watches/{id}/triage`) lists the requests with their answers.
- **Must-deliver events.** These are never triaged.

## Outcomes, marks and the report

A delivery accepts at most one owner outcome (`acted`, `dismissed` or `deferred`)
and at most one operator mark (`should_have_woken` or `needless_wake`). Owners
settle their own deliveries: an orchestrator or attempt uses the
`delivery_outcome` tool (fenced on the current generation), and the operator uses
`POST /api/deliveries/{id}/outcome` for operator-owned watches. Operator marks are
calibration evidence and apply to any delivered or digested group. The two
records are independent: one delivery can carry both, written in either order,
and each field is written at most once — repeating its value is idempotent, a
different one is refused. The records never change a receipt, and the runtime
never tunes thresholds from them.

`mitos watch report --id WATCH_ID` (or `GET /api/watches/{id}/report`) shows the
calibration state of one watch: delivery counts by level and by level source,
fallback reasons, outcomes, marks, triage calls used by request state, and two
derived ratios. **Wake precision** is one minus the share of marked wake
deliveries marked `needless_wake`; **fallback rate** is the share of graded groups
delivered by fallback. Both are omitted, not zero, until their denominators
exist, so an empty report cannot look calibrated.

## Default templates

`agents/watch-templates` ships three ready-made `watch_create` inputs for adaptive
orchestrators: **child-terminal-states** (children finishing, failing or being
cancelled, at `notify`), **verification-failures** (`verification_failed` at
`wake`) and **quiescence-steer** (`quiescent` steer edges at `wake`, with a
15-minute silence switch). A template leaves its scope empty; the runtime clamps
it to the creator's graph. The default orchestrator instructions point at them
when `watch.manage` is granted.

