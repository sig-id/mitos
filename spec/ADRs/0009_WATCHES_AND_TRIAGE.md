# ADR 0009: Runtime-evaluated watches with decision-model triage

Status: proposed, 2026-11-20; delivery tracked by gates W1–W4.
Extends [ADR 0003](0003_DECISION_MODELS_AND_RUNNER.md) and
[ADR 0007](0007_SESSIONS_ROUTING_AND_SUPERVISION.md).

## Context

Orchestrators are woken by "relevant task outcomes, accepted artifacts, failures,
decisions, and service inputs" ([03](../system/03_ORCHESTRATION.md#event-driven-coordination-and-replacement)),
but nothing declares which events are relevant, at what granularity, or for how
long. Today the engine watches each run deterministically: quiescence edges,
heartbeats and boundary waits record evidence, and a `block` edge escalates to the
operator. No agent watches anything. The responsibility inbox is written over HTTP
and displayed, but no path delivers it to the owning orchestrator session.

We reviewed the Claude Code `Monitor` tool (tools reference and CHANGELOG through
2.1.3xx, 2026-11-20). It streams each stdout line of an agent-authored script, or each
WebSocket text frame, into the conversation as a notification. Lessons adopted:

| Monitor behaviour | Lesson |
|---|---|
| The agent writes the filter; the harness owns deadline, volume cap and delivery | The agent declares the watch; the runtime evaluates and enforces it. |
| Every watch has a deadline; the no-timeout `persistent` option was removed (2.1.271) | Watches are leases, and expiry is always reported. |
| Lines within 200 ms batch; final output and exit merged "saving a model turn" (2.1.274) | Each delivery costs an owner turn, so group events before delivery. |
| A flooding monitor is stopped | Overload degrades to counts rather than failing or going silent. |
| Events are labelled as not user input | Watched content is untrusted evidence and cannot carry authority. |
| A filter matching only success stays silent through a crash | Terminal and absence states are delivered by rule, not by the author's filter. |

Rejected from Monitor: script stdout as the event source. Its output is not
journalled, cannot be replayed, and runs inside the confined workspace.

Prior art outside agent tooling solves the remaining parts. Kubernetes watches give
a cursor (`resourceVersion`), server-side timeouts and re-list on expiry.
Prometheus Alertmanager gives grouping (`group_by`, `group_wait`, `group_interval`,
`repeat_interval`). SRE practice gives dead-man's-switch absence alerts and triage
before paging. We adopt these instead of inventing per-line and summary modes.

## Decision

1. **A watch is a runtime record**, `mitos.watch/v1`: an owner, a journal-sequence
   cursor, a closed deterministic condition, a lease, a grouping policy, an
   optional maximum silence and optional triage instructions. The runtime evaluates
   it over the journal. Watches never run author code.
2. **Absence and terminal states are delivered by rule.** Run termination, `block`
   quiescence, `blocked_on_human`, `observation_failed`, supervision incidents,
   parked model decisions, lease expiry and watch degradation bypass both the
   author's condition and triage. A watch with `max_silence` reports silence.
3. **Delivery is graded**, with a total order `digest < notify < wake < escalate`.
   There is no drop level: every matched event is at least counted in a digest.
   Per-line and summary behaviour are points on this scale, not modes.
4. **Triage is a decision-model role.** Where configured, one typed Choice question
   per group chooses the delivery level from the levels the runtime offers. It uses
   the shared decision service ([08 §5](../system/08_AGENT_SESSIONS_AND_SUPERVISION.md#5-shared-decision-service))
   under its own role, policy and budget, as supervision does.
5. **Triage is asymmetric.** A model answer above the watch's default level is
   accepted without a threshold. An answer below it requires the configured
   confidence and probability thresholds. Refusal, invalid answers, timeouts, unknown
   outcomes after a restart and an exhausted allowance all use the default level.
6. **Triage has no execution effect.** It decides who is told and how urgently. It
   cannot steer, stop, continue, approve or accept work, and it cannot widen a
   watch's scope. Steering stays with runtime policy and supervision.
7. **Watches belong to responsibilities, attempts or operators, never to sessions.**
   Responsibility watches survive orchestrator replacement, fenced by generation.
   Attempt watches end when the attempt settles. An owner holds many watches, and an
   event can match many of them. Each owner receives an event once, at the highest
   level any of its matching watches assigns.

### Why triage is not a runner hook

ADR 0003 hooks run with no worker process alive, and an unresolved or failed
decision parks the run. A watch decides while work is live, and parking a healthy
run because the attention classifier failed inverts the safety direction. Triage
therefore fails toward more attention, and it never parks a run.

## Consequences

The orchestrator (System 2) declares attention once and System One answers a small
repeated question, so the expensive model sees only grouped and graded deliveries.
Replay reconstructs deliveries from receipts without calling models. Outcome records
for each delivery provide the application-specific labels ADR 0003 says thresholds
need.

Costs: a new journal record family, a condition vocabulary that must stay closed,
and an owner-delivery path that does not exist today. Triage budgets add a third
decision allowance beside routing and supervision.

This ADR widens ADR 0003's profile only by reusing Choice for a new role. Predicate
(Noul) and Score remain excluded until W4 defines their mappings; W1–W3 ship with
Choice only.

## Alternatives

- **Agent-run monitor scripts in the workspace.** Rejected: unreplayable, confined to
  the workspace's view, and they spend model turns on raw lines.
- **Separate per-line and summary modes.** Rejected: one graded scale with grouping
  covers both and lets triage choose for each group.
- **Triage that can drop events.** Rejected: silence would look identical to health.
- **Orchestrator polling of run state.** Rejected by 03, which forbids model calls on
  scheduler ticks.
- **Model-written condition expressions (CEL, jq).** Deferred: a closed filter is
  enough for W1, and an expression language needs its own bounds and disclosure rules.

The owning contract is [watches and triage](../system/09_WATCHES_AND_TRIAGE.md);
delivery is defined in [06](../system/06_DELIVERY_AND_CONFORMANCE.md#watch-and-triage-delivery-gates).
