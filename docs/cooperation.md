# Cooperation in Mitos

`AGENTS.md` supplies project instructions. Optional root `COOPERATION.md` describes
team conventions; new coordinated work freezes its contents/hash. Instructions
and cooperation messages never grant permissions.

## v2 contract

`POST /api/projects/{project}/cooperation` records a local operator-authored
`mitos.cooperation/v2` envelope. Native agents use `cooperation_submit` from the
[handoff API](agent-handoffs.md); the runtime binds their producer identity.

```json
{
  "version": "mitos.cooperation/v2",
  "id": "investigate-failure-1",
  "task_id": "task_origin",
  "run_id": "run_origin",
  "revision": 1,
  "kind": "help",
  "body": "Diagnose the retained test failure before I finish.",
  "artifacts": ["artifact_REGISTERED_ID"],
  "permitted_resolvers": [
    {"task_id": "task_reviewer", "run_id": "run_reviewer", "revision": 1}
  ],
  "blocked_actions": [
    {"task_id": "task_origin", "run_id": "run_origin", "revision": 1,
     "action": "acceptance"}
  ],
  "expires_at": "2026-12-01T12:00:00Z"
}
```

Use a future expiry within 30 days. IDs are project-scoped idempotency keys: replay
of identical content returns the original receipt; conflicting reuse fails. The
receipt includes journal sequence and time. Envelopes are capped at 24 KiB encoded,
with 16,000-byte bodies, at most 32 artifact IDs and 16 resolvers/blocked actions.
Artifact references must be intact and accessible to the originating attempt.
They grant the named resolver attempts access to those exact objects while the
request is pending/answered; no other producer files are disclosed.

Kinds are `assignment`, `help`, `question`, `approval`, `observation`, `handoff`,
`result`, `answer` and `cancel`. A cooperation `result` is an informational message,
not a substitute for `result_submit`. `help`, `question` and `approval` require
explicit currently running resolvers; cross-task resolution is supported within
the same graph only. A resolver binds task, run and revision, not an agent-chosen
role name. Session-origin observations remain possible; v2 request settlement
requires a run-bound identity.

`answer` and `cancel` specify `in_reply_to`. Only a listed resolver can answer;
only the original identity can cancel. Competing settlements are atomic. A duplicate
returns its prior receipt, while a second distinct settlement conflicts. Expired,
cancelled, stale-revision, cancelled-task and replaced-attempt requests cannot be
answered. States are `pending`, `answered`, `cancelled`, `expired`, `superseded` or
`unsupported_version`. Settled answers remain recorded as evidence after the
producer exits. Pending requests never transfer to a retry, and no automatic
adoption/rebinding is supported.

This version supports blocking the originating attempt's `acceptance` only, using
its concrete task/run/revision. Completion and graph acceptance check that the
request was answered. Cancelling or expiring a blocking request does not silently
approve its work; use explicit task recovery/replacement. The existing scheduler's
candidate/accepted dependencies therefore stay blocked; messages introduce no
second readiness model or arbitrary predicates.

An `approval` message must reference an existing canonical `decision_id`. Its
answer records cooperation evidence only. It cannot resolve that decision or
create publication authority. Native approvals continue through the existing
agent bridge, with current policy/supervision rechecked after failed delivery as
well as before first delivery. Answering, delivering and applying remain separate.

`GET /api/projects/{project}/cooperation?run_id=RUN&cursor=0&limit=16` returns
bounded pages and `next_cursor`; omit `run_id` for project history. Native
`cooperation_list` restricts results to the caller's own/addressed requests and
replies. Re-list from zero to refresh expiry/supersession: sequence cursors advance
immutable events, while lifetime states are projections of the same durable runs.
New writes reject v1. Historical records remain readable without a legacy handler.
