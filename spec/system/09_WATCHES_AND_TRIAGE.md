# 09 – Watches, delivery, and triage

This contract defines how an owner declares which runtime events deserve its
attention, how the runtime groups and delivers them, and how a decision model may
grade that delivery. It implements the relevance and coalescing obligations of
[event-driven coordination](03_ORCHESTRATION.md#event-driven-coordination-and-replacement).
Requirements are targets until gates W1–W4 record evidence. Rationale is in
[ADR 0009](../ADRs/0009_WATCHES_AND_TRIAGE.md).

## Vocabulary

| Term | Meaning |
| --- | --- |
| Watch | A durable, leased subscription evaluated by the runtime over the journal |
| Owner | The responsibility or operator that receives a watch's deliveries |
| Group | The events of one watch, within one group key, that are delivered together |
| Delivery | One immutable, graded receipt for an owner, referencing contributing events and recording where it was routed |
| Inbox item | A delivery queued in a responsibility's or attempt's inbox, pending until the owner records its outcome |
| Operator item | A Notifications item opened by one delivery and updated by later ones for the same watch and group key |
| Triage | The decision-model role that selects a delivery level for a group |

## 1. Watch record

A watch is `mitos.watch/v1`. Unknown versions MUST be rejected; there is no
unversioned form.

```json
{
  "version": "mitos.watch/v1",
  "id": "watch_…",
  "owner": {"kind": "responsibility", "id": "graph_…"},
  "description": "integration failures in the payments graph",
  "cursor": 18231,
  "condition": {
    "scope": {"project": "project_…", "graph": "graph_…"},
    "types": ["effect"],
    "kinds": ["verification_failed", "quiescent", "harness_session_recorded"]
  },
  "lease": {"expires_at": "2026-11-21T09:00:00Z", "max_renewals": 16},
  "grouping": {"by": ["run_id"], "wait_ms": 2000, "interval_ms": 60000, "max_events": 64, "repeat_interval_ms": 3600000},
  "max_silence_ms": 900000,
  "default_level": "notify",
  "triage": {"instructions": "Wake only when the failure changes which task should run next.", "max_calls": 32, "allow_escalate": false}
}
```

- **Owner.** A responsibility, an attempt, or the operator. Watches MUST NOT belong
  to a session. A responsibility is an adaptive graph's supervisory responsibility,
  addressed by the graph ID (the same address as its mailbox); its generation is the
  graph's orchestrator generation. A defined graph has no orchestrator and admits no
  responsibility owner. An attempt is one graph run. A responsibility's watches survive
  orchestrator replacement, and every delivery binds the responsibility generation
  current when it is written. An attempt's watches are clamped to that attempt's grant
  and end when the attempt settles (completes, fails or is cancelled), whatever their
  lease. This mirrors agent-owned monitors that stop with their agent.
- **Grant.** A responsibility or attempt owner may watch only its own graph. The
  runtime records that grant on the watch and clamps the scope to it: an unset project
  or graph is filled from the grant, and a conflicting value is refused. Closing the
  graph (accepted, cancelled or failed) revokes the grant.
- **Multiplicity.** Owners and watches are one-to-many, and watches and events are
  many-to-many. An owner holds at most 32 active watches, and the operator at most 256.
  When an event matches several watches of the same owner, the owner receives it once.
  The receipt lists every matching watch, and its level is the highest level any of
  them assigns. Different owners receive their own deliveries independently.
- **Cursor.** The journal sequence after which the watch has consumed events. The
  cursor advances only in the same transaction that writes the delivery or digest
  receipt for those events, so no event is lost or delivered twice.
- **Condition.** A closed filter on scope (project, graph, task, run, responsibility),
  event `type` (`effect` or `decision`), and event `kind` (at most 32). At least one
  scope field is required. The scope MUST be within what the creator may read.
  A watch cannot match events outside that scope, including after the creator's
  grant changes. Revocation ends the watch with a must-deliver notice. There are no
  expressions, regular expressions or free-text matches in v1.
- **Lease.** Every watch has an absolute expiry, at most 24 hours after creation or
  renewal. Renewal is an explicit owner or operator operation, bounded by
  `max_renewals` (1–1024). Expiry ends the watch and produces a must-deliver notice
  carrying the count of events matched since the last delivery.
- **Grouping.** `by` takes up to three fields from `run_id`, `task_id` and `kind`.
  A group opens on its first matching event, is first delivered after `wait_ms`
  (0–60000), and then at most once per `interval_ms` (1000–3600000) while events
  continue. `max_events` (1–256) closes a group early. Continuous updates MUST NOT
  postpone delivery past `interval_ms` (no starvation). A group holding a `wake` or
  `escalate` must-deliver event is due immediately. A delivery MUST retain
  the identities of all contributing events (sequence range plus counts by kind).
  `repeat_interval_ms` (60000–86400000) bounds how often a new operator item is
  created for the same group key (§3).
- **Maximum silence.** If set (60000–86400000), a window with no matching events
  produces a must-deliver `watch_silent` notice. This is the absence signal; quiet
  output alone is not evidence of a stall ([04](04_STATE_AND_VERIFICATION.md#observation-and-steering)).

Creation, renewal, cancellation and expiry are journal events. A watch is created
through the runtime API by an authorized operator, or by an orchestrator through a
granted `watch_create` tool whose scope is clamped to its responsibility's graph.

## 2. Must-deliver events

These events bypass the author's condition and triage for any watch whose scope
contains them. They are delivered at the level shown, or at the watch's default
level if that is higher:

| Event | Level |
| --- | --- |
| Run terminal: completed, failed, cancelled, parked | `notify` |
| Quiescence `block` edge, `blocked_on_human` | `wake` |
| `observation_failed`, supervision incident or hold | `escalate` |
| Model decision parked or outcome unknown | `wake` |
| Watch lease expiry, `watch_silent`, `watch_degraded`, scope revocation | `notify` |

The list is runtime-owned, versioned with `mitos.watch/v1`, and cannot be narrowed by
an author.

## 3. Delivery levels and sinks

Levels are totally ordered: `digest < notify < wake < escalate`. No level discards
events.

| Level | Effect |
| --- | --- |
| `digest` | Counted into the owner's next digest; no message per group |
| `notify` | Appended to the owner's inbox; no wake |
| `wake` | Appended to the inbox and wakes the owner through its wait registration |
| `escalate` | `wake`, plus an operator item in the Notifications surface |

Every delivery is a receipt with one route: `feed` (the operator's delivery feed),
`inbox` (an inbox item for a responsibility or attempt), or `folded` (counted into
that owner's digest and never queued). A delivery may also open or update an operator
item and records that item's ID. The receipt never changes after it is written. Item
state (open, resolved, superseded) and inbox state (pending, outcome) are separate
records, keyed by their own ID and by delivery ID respectively, and they are written
in the same journal record as the receipt.

For operator-owned watches, every delivery goes to the delivery feed, and `wake` and
`escalate` deliveries also open or update operator items. For other owners, `escalate`
deliveries also open or update operator items.

**Wait registration.** A responsibility is dormant between planning rounds: no
session waits on it. Its wait registration is the graph trigger, the same durable
record that run outcomes use to reopen planning. A `wake` or `escalate` delivery
writes a graph trigger in the same journal record as its receipt. The kernel opens
the next planning round from that trigger and classifies it when the round's plan is
applied. A wake is therefore never lost to a restart or to orchestrator replacement,
and it is applied once. The planning round's context ends with a compact summary of
the pending deliveries: counts by level, the digest totals, and as many of the
newest receipts (at most eight) as the context limit leaves room for. The summary
never prevents a planning round from starting; the complete receipts stay available
through `watch_inbox`. An attempt owner is live while it owns watches, because they
end when it settles. For an attempt, a wake is the inbox append, which the attempt
reads with `watch_inbox`. The S4 task-input wait applies only to structured-worker
attempts and does not carry watch deliveries.

Each responsibility and attempt owner has a bounded inbox of delivery receipts in
the journal. This is separate from the interactive workspace's responsibility notes.
- `digest` deliveries are always folded into the pending digest.
- When the inbox holds 64 queued receipts, further `notify` deliveries fold too. A
  single `watch_degraded` notice, exempt from the capacity, records each fold episode.
- `wake` and `escalate` deliveries keep queueing up to 128 receipts. Beyond that they
  fold too, but they still wake their owner.
- Any inbox item carries the pending digest and clears it.
- A digest that nothing has carried is delivered after the shortest `interval_ms`
  among the watches folded into it. The digest keeps that interval, so it is still
  delivered after those watches end.

The previous hard "inbox is full" error MUST NOT apply to watch deliveries.

The owner receives deliveries as `mitos.cooperation/v2` `observation` messages
bound to responsibility, generation, watch, sequence range and level. Delivered text
is evidence. It MUST be presented as untrusted data, and it cannot carry
instructions, grants or tool calls. The runtime renders receipt fields itself and
places the author's description in a field marked untrusted. Reading deliveries
writes nothing. A delivery is acknowledged by recording its outcome. For a
responsibility, the acknowledgement must carry the current orchestrator generation:
a replaced orchestrator's acknowledgement is rejected as stale. Repeating the same
outcome is idempotent, and a different second outcome is rejected.

Operator items are bounded the way kernel judgments bound operator cards. An
`escalate` delivery for a watch and group key that already has an open operator item
updates that item's counts and sequence range instead of creating a new one. A new
item is created only after the previous one is resolved, or after the watch's
`repeat_interval_ms` (default 3600000) has elapsed. A run parked for a kernel
judgment already has its operator card, so its must-deliver notice goes only to the
owner, at `notify`, and creates no second operator item.

A digest is delivered when the owner next wakes, or at a watch's `interval_ms`
if it has no other delivery. It contains counts by kind and group, plus the sequence
range.

## 4. Triage

Triage is optional. Without it, every matched group is delivered at `default_level`.
A watch's `triage` block holds only the author's `instructions` (1–4000 bytes), an
optional `max_calls`, and `allow_escalate`. The author chooses neither the provider
nor the thresholds.

**Provider.** Triage uses the owner's existing decision provider, the same one its
other decisions use. For a responsibility or attempt, that is the graph's
`decision_application`, or else the project policy's provider. For the operator, it
is the project policy's provider (`decision_provider` or `decision_application`,
falling back to the system provider). The provider's `min_confidence` and
`min_probability` are the triage thresholds. Its `timeout_ms` is capped at 30000,
and its runner `hooks` play no part. Without a configured provider, a group is
delivered at `D` with `provider_unconfigured`, and no call is counted.

**Question.** One Choice question per group, named `level`, using the shared
decision service ([08 §5](08_AGENT_SESSIONS_AND_SUPERVISION.md#5-shared-decision-service))
and the existing provider adapters ([05](05_INTERFACES_AND_INTEGRATIONS.md#decision-model-provider-profile)).
The runtime offers `digest`, `notify` and `wake`. It offers `escalate` only when the
watch sets `allow_escalate` or its default is `escalate`. Escalate grants nothing that
the author could not already get by setting `default_level`. Triage is never
invoked for a group that holds a must-deliver event.

**One question per group.** A request holds the events it was asked about. Until
its outcome is recorded, no second request is opened for that group, and later
routine events wait. A must-deliver event is still delivered at once, untriaged,
together with the events that the request does not hold. The outcome delivers
the held events that are still undelivered, and no others.

**Projection.** Watch ID and description, the author's triage instructions (at most
4000 bytes), group key, event counts by kind, sequence range, up to eight
runtime-authored evidence summaries with their kinds, the owner's current pending
delivery count, and the remaining triage allowance. It excludes event payloads, raw
output, transcripts, workspace contents and credentials. Evidence summaries written
from worker output are untrusted data.

**Acceptance.** Let `D` be the watch's default level and `A` the model's answer.

- If `A ≥ D`, deliver at `A`.
- If `A < D`, deliver at `A` only when both the configured confidence and the selected
  probability meet the provider's thresholds. Otherwise deliver at `D`
  (`below_threshold`).
- An answer outside the offered set is invalid.
- Every failure delivers at `D` and records its reason: `refused`, `invalid_answer`,
  `timeout`, `provider_error`, `credential_unavailable`, `allowance_exhausted`,
  `outcome_unknown` or `provider_unconfigured`. The grading is a pure function of the
  answer, the error, `D`, the offered set and the thresholds.

Triage MUST NOT park, steer, stop, continue, approve or accept work. It selects only a
level from the offered set.

**Durability.** The runtime persists the triage request before dispatch and the
outcome before the delivery receipt. Replay uses recorded outcomes and never calls
models. A request that is still pending at restart is not resent: the group is
delivered at `D` with `outcome_unknown`. A call that outlives its deadline is
recorded as a timeout. The outcome and its delivery are one journal record. Every
delivery records its `source` (`rule`, `default`, `triage` or `fallback`), its
`fallback` reason, and the `triage_id` it settled. Triage records are readable at
`GET /api/watches/{id}/triage`.

**Budget.** Triage draws on its own allowance:
- A graph declares `Limits.TriageCalls` (0–4096) beside its other finite limits
  ([02](02_RUNTIME.md#attempts-dependencies-and-iteration)). Watches owned by that
  graph's responsibility and attempts share it.
- Operator-owned watches use the project policy's `triage_calls` (0–4096). It is
  counted per policy revision, so saving the policy is an explicit operator refill.
- A watch may narrow its share with `max_calls` (1–1024). That limit covers the
  watch's whole life: renewal, orchestrator replacement and new attempts MUST NOT
  replenish it.
- Each request is bounded by the provider's `timeout_ms`, capped at 30000.

Pending and failed requests count. When an allowance is exhausted, the watch emits
one `watch_degraded` notice and continues untriaged at `D`. As with judgment-shaped
kernel rules ([03](03_ORCHESTRATION.md#judgment-shaped-kernel-rules)), an exhausted
or failed decision keeps the default, and a decision only changes the outcome.

Triage MUST NOT charge `Limits.OrchestratorCalls`, which is shared by planning,
graph decisions and kernel judgments, and those consumers MUST NOT charge
`TriageCalls`. A noisy watch therefore cannot starve planning or judgments, and a
flood of judgments cannot disable triage.

## 5. Outcomes and calibration

Each delivery accepts at most one owner outcome: `acted`, `dismissed` or `deferred`.
An operator may mark a delivered or digested group as `should_have_woken` or
`needless_wake`. These records are evidence for tuning triage thresholds for a
specific application and model, and the runtime MUST NOT adjust thresholds
automatically from them. A watch report exposes counts by level, fallback reasons,
outcomes, and triage calls used.

## 6. Interfaces

- HTTP: `POST /api/watches`, `GET /api/watches/{id}`, `POST /api/watches/{id}/renew`,
  `POST /api/watches/{id}/cancel`, `GET /api/watches/{id}/deliveries`,
  `GET /api/watches/{id}/items`, `GET /api/watches/{id}/triage`,
  `POST /api/watch-items/{id}/resolve` (closes an
  open operator item), and `POST /api/deliveries/{id}/outcome` (W4; settles an inbox
  item). All require the `Mitos-API-Version`
  header. Unknown request fields are refused.
- `GET /api/watch-inboxes/{responsibility|attempt}/{id}`: an owner's pending
  deliveries and digest, read-only.
- Agent tools, granted by the `watch.manage` capability only: `watch_create`,
  `watch_renew`, `watch_cancel`, `watch_inbox`, `delivery_outcome`. Their scope is
  clamped to the caller's graph. Only the current orchestrator generation acts for
  the responsibility, and any attempt may own watches for itself.
- CLI: `mitos watch create|list|show|renew|cancel|deliveries|items|triage|resolve|inbox`.

## 7. Out of scope for v1

Predicate (Noul) and Score questions; expression conditions; capability-declared
watches in manifests; triage-selected steering or stopping; cross-project watches;
external sources (WebSocket, MQTT topics) other than journalled service inputs.
W4 defines which of these are admitted next.
