# Watches

A watch tells you when runtime events in a project, graph, task or run need your
attention. The runtime evaluates it over the journal; nothing runs in a workspace.
The contract is [spec 09](../spec/system/09_WATCHES_AND_TRIAGE.md). This release
implements gate W1: operator-owned watches without triage.

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
mitos watch resolve --delivery delivery_…
```

The HTTP API is under `/api/watches` and needs the `Mitos-API-Version` header.
Unknown fields are refused.

## What you receive

- **Matching events** are grouped by the `grouping.by` fields. A group is
  delivered `wait_ms` after its first event, then at most once per `interval_ms`
  while events continue, or immediately when it reaches `max_events`.
- **Must-deliver events** reach every watch whose scope contains them, even
  when its `kinds` filter does not match: finished, failed, cancelled and parked
  runs; quiescence `block` edges; `blocked_on_human`; `observation_failed`;
  supervision incidents; parked model decisions. Rules at `wake` or `escalate`
  are delivered immediately.
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
neither loses nor repeats it.

## Not yet available

Watches owned by an orchestrator's responsibility or by an attempt, the
`watch_*` agent tools, and decision-model triage are later gates (W2–W3). The
runtime refuses those fields instead of ignoring them.
