# Adaptive work graphs

The backend composes existing Tasks and Run attempts into durable, mutable work
graphs. An operator, registered controller application, or deterministic client can
discover tasks during execution. The scheduler launches eligible work without a
supervisor issuing individual launch commands. No worker reply tags are required.

This is a local application adapter for the `work-graph/v1` design. Discover its
actual coverage at `GET /api/graphs/capabilities`; `complete_profile` is **false**.
The project **Work** page creates these graphs from a free-text goal and displays
their evolution. Existing native sessions remain under **Sessions & earlier work**;
service activations do not yet create graph templates.

## Work page

1. Describe a goal. Choose a planner, or **I’ll add the steps** for manual control.
   Optional success criteria and executor grants are set before creation.
2. Watch goal, planner, decision, and application nodes evolve. Solid edges show
   dependencies; dashed edges show containment. Select a node for its assignment,
   saved output, artifacts, and attempts. Collapse scopes, zoom, or use list view.
3. Add steps, inspect failures, retry eligible attempts, replace the planner, or
   stop work. Changes use the backend's revision checks and idempotency keys.
4. Review a settled result against its criteria and supply evidence. Closing
   planning and accepting the root is explicit; an empty queue is not completion.

The page polls one selected graph's lightweight status projection each second and
summary lists every five seconds. Polling pauses in hidden tabs and backs off on
connection errors. Output is fetched only for the selected step and artifact;
saved output is available after the executor finishes. This is not live PTY output.
A lost mutation response is retained in session storage for exact-key reconciliation,
including after reload. A stale review must be reopened against the latest state.

The initial UI creation limits are 64 tasks, 96 attempts, 24 planning calls, and a
two-hour deadline. They bound this activation; advanced limits and graph operations
remain available through the API. The graph editor supports adding application
steps and dependencies, not every operation in the runtime wire schema.

## Planner setup

Register a controller application using the protocol described below and set
`"graph_controller": true` in its `app.json`. Restart the server to discover the
manifest. This flag advertises a compatible planner in the Work page; it is not an
additional authority grant. Existing API clients may continue explicitly binding
registered controllers without the flag. Ordinary worker applications are never
automatically presented as planners.

The controller receives `graph.context` on stdin and writes `graph-proposal.json`
with proposed operations and an explanation. It may wrap an agent CLI, use a model,
or run deterministic code. An installed interactive Codex terminal alone is not a
registered graph controller. Without a compatible planner, the page offers manual
steps and links to **Applications → Set up graph planning**.

## Implemented behavior

- One home/project, one logical root scope, immutable goal/criteria policy,
  required obligations, nested scopes, explicit dependencies, and all/any/k-of-n joins.
- Atomic multi-operation amendments, structural compare-and-swap, separate fact
  revisions, proposal read sets, request deduplication, and stable committed IDs.
- Registered application templates, per-task context/assignment, and optional
  generated argv additions. Commands, provider requirements, and acceptance policy
  are frozen; argv changes require the graph's `allow_arguments` grant. A registered
  application can wrap an agent CLI or invoke ordinary tools, including Git.
- Automatic dispatch through the existing two-slot pool/claim scheduler. Logical
  scopes and planning waits consume no process slots. Planning and decision calls
  have real Task/Run records. Queued attempts consume the shared attempt allowance.
- Accepted-result, terminal failure/cancellation, and verifier `candidate_available`
  edges; existing artifact-copy validation; dependency witnesses frozen at admission.
- New attempts for retries, new tasks for changed work, supersession lineage,
  retained evidence, cancellation intent, and shared finite responsibility limits.
- Outcome-triggered planning, fenced controller replacement, optional typed model
  choice over validated candidates, and durable inference/application receipts.
- Explicit scope closure and operator root acceptance. Finishing a command, an empty
  queue, or an agent's narrative never accepts the root goal.

A task with `acceptance: application` accepts a successful result only when its
registered application verifier passes. `acceptance: operator` keeps that result
as an unaccepted candidate until explicit review. Verifier policy is trusted local
configuration, not enforcement of arbitrary natural-language criteria.

## HTTP contract

These routes use the existing localhost/operator trust boundary, cross-origin
protection, and home-server checks. Controller execution is trusted local code;
this implementation does not introduce a sandbox or multi-tenant authorization.

| Method and route | Operation |
|---|---|
| `GET /api/graphs/capabilities` | Implemented adapters and limitations |
| `GET /api/graphs/applications` | Registered executor choices and planner discoverability |
| `GET /api/graphs/{id}/snapshot?node=…` | Status projection; assignment details only for root/selected node; no executor history |
| `GET /api/graphs/{id}/nodes/{task}/runs/{run}` | Bound attempt details without events or frozen execution configuration |
| `POST /api/graphs` | Idempotently create a concrete graph/activation |
| `GET /api/graphs?project_id=…&limit=50&after=…` | Bounded summaries; explicit continuation/truncation |
| `GET /api/graphs/{id}` | One consistent bounded graph snapshot, receipts, decisions and history |
| `GET /api/graphs/{id}/blockers` | Unaccepted work, open scopes, unresolved planning and execution |
| `POST /api/graphs/{id}/preview` | Validate an amendment without committing or reserving IDs |
| `POST /api/graphs/{id}/amendments` | Commit an amendment and return stable IDs/receipt |
| `POST /api/graphs/{id}/decisions` | Queue a bounded candidate-selection inference |
| `POST /api/graphs/{id}/decisions/{key}/apply` | Revalidate and apply a recorded choice; body `{}` |

The typed wire schemas are `CreateGraph`, `GraphProposal`, `GraphOperation`, and
`GraphDecisionRequest` in `internal/studio/graph_model.go` and `graph_decisions.go`.
Mutation requests are limited to 2 MiB. Unknown fields/operations, duplicate JSON
keys, invalid UTF-8, unpaired Unicode surrogates, and trailing JSON are rejected.
Integer fields require integer JSON syntax. Identical requests use SHA-256 of the
Go typed JSON encoding: struct declaration order, sorted map keys, decoded Unicode
strings without normalization, typed timestamp encoding, and declared `omitempty`
rules. Field order and equivalent Unicode escapes do not change the digest;
changing content under an existing key returns 409. Request digests are computed by
the server. Preview IDs are provisional; only committed receipt IDs can be reused.
`TestGraphStrictJSONAndCanonicalDigest` exercises these representation rules.

Creation keys are home-wide. Use a unique activation key for each scheduled/event
occurrence; repeating the same key and content returns the existing graph. The
`activation` string records provenance. It does not itself install a timer or event
subscription. Terminal goal revisions can be linked with `predecessor`; they are
never reopened or silently reused for a new occurrence.

## Create work

Open a project through the existing project API/UI. Substitute its ID below and a
future RFC3339 deadline. Application IDs must already be installed on the server.

```json
{
  "profile": "work-graph/v1",
  "key": "checkout-investigation-1",
  "project_id": "PROJECT_ID",
  "goal_revision": 1,
  "activation": "manual",
  "policy": {
    "mode": "adaptive",
    "applications": ["workspace-inventory"],
    "allow_arguments": false
  },
  "limits": {
    "tasks": 32, "amendments": 64, "attempts": 48, "retries": 2,
    "expansions": 16, "controller_calls": 16, "depth": 8, "fanout": 16,
    "deadline": "REPLACE_WITH_FUTURE_RFC3339"
  },
  "tasks": [{
    "id": "goal", "kind": "scope", "required": true,
    "criteria": "Explain the project with reviewed evidence",
    "acceptance": "operator",
    "brief": {"title": "Understand the project"}
  }],
  "seal": false
}
```

An adaptive/hybrid graph begins with an `initial` expansion obligation. With no
controller application configured, a caller supplies amendments. Read the graph,
then POST an envelope containing `profile`, a fresh `key`, `graph_id`, `revision`,
`policy_revision`, `generation` (the current `controller_generation`), `deadline`,
and `operations`. A typical first amendment contains:

```json
[
  {
    "kind": "add",
    "spec": {
      "id": "inspect", "parent": "ROOT_TASK_ID", "kind": "application",
      "required": true, "criteria": "Inventory independently verified",
      "acceptance": "application",
      "brief": {
        "title": "Inspect the project", "app_id": "workspace-inventory",
        "timeout_minutes": 2
      }
    }
  },
  {"kind": "close_expansion", "expansion": "initial", "rationale": "Start with an inventory"}
]
```

`add` IDs are proposal-local; parent/dependency references can use them within the
same amendment. Add parent scopes before children. The receipt maps local IDs to
stable Task IDs. The registered project supplies the workspace. Executable task
specifications freeze as soon as an attempt is durably queued; subsequent changes
require supersession. Catalog snapshots freeze at specification resolution, and
input witnesses freeze later at scheduler admission. Existing attempts keep them.

Supported operation kinds are `add`, `amend`, `dependencies`, `priority`,
`supersede`, `retry`, `open_expansion`, `close_expansion`, `classify_trigger`, `seal`,
`reopen`, `cancel`, `replace_controller`, `dismiss_decision`, and `accept`.
A `dependencies` operation replaces the pending consumer's dependency/join set.
Supersession preserves requiredness, criteria, acceptance, and parentage. Cancelling
or superseding work does not discharge its obligation or release process claims.
Weakening criteria/grants or discharging required work is deliberately not supported
by ordinary graph amendments; create a separately reviewed goal revision instead.

After outcomes arrive, classify each relevant trigger with a rationale, resolve
expansions, then seal scopes using the exact graph `cursor`. To accept a scope,
provide evidence/rationale and a `reads` entry for **every** descendant and the scope:
`{"TASK_ID":{"specification":1,"fact":4}}`. Acceptance must be the last operation.
All required obligations must be accepted (possibly through an accepted replacement),
and all execution/claims must be settled. Acceptance records an exact manifest.

## Controllers and native output

Set `policy.controller_application` to an installed application also listed in
`policy.applications`. Mitos schedules it when an expansion needs planning or a
worker outcome introduces a relevant trigger. Its stdin has the normal application
input plus `graph.context`, a JSON string containing bounded goal/task facts,
result summaries, artifact references, policy, and triggers. Settled worker output
and report artifacts are delivered through the existing immutable dependency-input
mechanism. Workers never need to know this controller protocol.

The controller may use any planning technology. It writes **`graph-proposal.json`**
in its workspace with an object containing `operations` and `explanation`. Mitos
captures this file from the harvested snapshot, waits for the controller's registered
verification, and validates the proposal. Native stdout remains `output.txt` and
is never executed or parsed as control. A controller's typed tool interface is
separate from its normal prose and from ordinary worker output.

The kernel closes the controller's expansion and classifies the triggering cursor
in the same amendment. A controller must not duplicate those operations. A stale or
malformed proposal leaves a visible review blocker. `replace_controller` fences the
old generation, requests cancellation of superseded planners, and rebuilds planning
from durable facts without resetting budgets or restarting existing workers.
Controller capacity is shared with workers. Planning is serialized per graph; the
runtime does not hold a process slot merely to supervise children.

## Optional model decisions

`policy.decision_application` names an installed application's existing
`decision_model` configuration. This selects the registered provider/model and
credential **reference**, never a caller-supplied endpoint. The graph decision
path uses the existing Choice adapters. Batch segment/recovery hooks are not enabled
inside graph worker attempts because their old per-run allowances would reset the
responsibility budget.

Submit a `GraphDecisionRequest` containing an empty-operation proposal envelope,
2–16 `candidates` (`operations` plus an explanation), and bounded disclosed
`context`. The backend validates all candidates, reserves an expansion and a real
Task/Run, then schedules one inference. Applying a selected answer is a separate
idempotent operation that rechecks revision, facts, generation, policy, and deadline.
Confidence never authorizes new actions or establishes goal acceptance.

Pending inference after a crash becomes `interrupted_unknown_consumption`. It is
not resent. Failed/low-confidence responses remain evidence; explicit review may
`dismiss_decision` or request a new bounded evaluation. Graph call/attempt limits
survive retries and controller replacement; token usage is observed, not an enforced
monetary reservation. Configuring automatic controller proposal application does
not implicitly enable model routing: clients explicitly request typed choices.

## Bounds and remaining integration

The local adapter permits at most 256 graphs; each graph has at most 512 members,
2,048 amendments/attempts, 256 expansions/controller calls, depth 32 and fanout 128,
with a deadline no more than 30 days after creation. Actual limits are explicit and
usually smaller. A complete journal record is capped at 8 MiB. Exhaustion refuses
new work while retaining history. Archival and a 1,000-agent capacity claim are not
part of this implementation. Existing two-worker execution capacity still applies.

PTY sessions retain their existing desk lifecycle and are not graph executors yet.
Agent CLIs can participate through explicitly installed application wrappers; their
credential/tool setup remains that application's responsibility. Cross-graph input
imports, remote placement, graph-native service templates, autonomous root acceptance,
policy weakening/discharge, and complete G1–G5 conformance remain integration work.
The current service scheduler still creates its existing task instances. No frontend
workflow or running demo process is replaced by these backend changes.

The provider remains same-user local execution. Restart parks interrupted processes
and retains unresolved custody instead of claiming exactly-once process launch.
