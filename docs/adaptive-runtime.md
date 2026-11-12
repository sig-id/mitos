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

## Automatic workflow

1. Open a project and describe a goal. **Start work** atomically creates a bounded
   graph with a planning obligation. Codex uses its existing local authentication.
2. A read-only planner chooses agents from the library and proposes assignments,
   dependencies, retries or replacements. A task-specific role can specialize a
   base definition without changing invocation, policy or permissions.
3. The scheduler admits real Codex attempts into its existing two-slot work pool.
   Workers run in fresh workspace copies. Bound upstream reports and patches are
   supplied as input; clean patches are applied before launch. Conflicting patches
   are reported to the worker for reconciliation. Originals remain untouched.
4. Workers return ordinary text. Reports, logs, changes and workspace archives are
   saved separately. The planner reads these artifacts, evaluates the goal and
   criteria, and either extends the graph or accepts the evidenced result. Worker
   exit status is not a verifier verdict. Completion is labelled agent-reviewed.

The project has stable **Conversation**, **Workflow**, **Result** and **Changes**
views. Conversation is the default entry and shows the request, progress and answer
preview. Result is a dedicated view; Changes compares the project checkout or a
selected saved agent patch without entering the older native-session interface.
**Automations** configures repeated plugin jobs (not adaptive graph occurrences);
**Project setup** holds project context, device information and session limits.

The planner chooses a presentation suited to the request: narrative, checklist,
table, saved files/diffs and labelled links to actual issues or pull requests.
`result.items` contains bounded `file`, `changes` or `link` items; source-bound file
items and HTTP links are validated, and the UI never executes generated HTML.
Ordinary worker output remains unconstrained prose. Formatting the final answer
uses the existing planner rather than requiring another review task.

After completion, the message composer starts a continuation with `from_graph` and
`from_revision`. The creation key deduplicates uncertain retries. The predecessor
stays settled; the continuation gets fresh bounded work allowances and freezes the
prior answer and its accepted source runs. Planner and worker attempts receive
checksum-verified reports, manifests, patches and archives in a separate read-only
input directory. Old patches are not implicitly applied. External publication must
be expressly requested by the current user; saved output cannot authorize it.
Issue/PR creation still needs available native tools, credentials and network access.
Active work currently exposes progress and stop controls; the follow-up composer
sends once an answer is ready, rather than implying that messages steer an already
running native attempt.

The left work tree selects goals and agents. One Planner entry groups planning
rounds into a chronological history; the graph shows worker assignments and their
dependencies. Replaced assignments are available through **Show replaced work**.
The graph also supports zoom, collapse and a list view. Selecting an agent opens its output in a wide reader, which can
expand to the full content area. Planner proposals become readable summaries and
expandable assignments; raw data is available separately. Attempt history opens
saved output and files in place, with technical execution pages under Diagnostics.
**View result** opens the goal's saved final answer, separately from planner
acceptance notes and worker reviews. Automatic completion requires a bounded
`result` with a title, Markdown answer and accepted worker `source_tasks`; the
runtime freezes those sources to their accepted run IDs. The planner synthesizes
the answer to the original request from existing evidence. For audits, this means
concrete findings, requirements, evidence and limitations, rather than review status.
Older accepted graphs without an answer say so; an operator can publish a missing
answer or a corrected revision through `POST /api/graphs/{id}/result` with the expected graph revision,
title, Markdown and source task IDs. Publication does not rerun or reaccept work.

Worker reports, patches and workspaces remain under supporting evidence. Saved-file
citations, including inline code paths and source line references, open inside the
reader with back navigation. `mitos://TASK_ID/path#L123` pins an answer citation to
its accepted source run. Relative report links resolve against that report's saved
workspace. Both changed files and unchanged source files are read from the
checksum-verified archive; changed files also match their change-manifest digest.
No link reads the current project or mutable worker directory. Markdown checklists
show the authored finding state; audit completion does not mark gaps fixed.

The work tree and graph use the same dependency order, with stable ordering for
parallel assignments.
Only the selected graph is polled each second; summary status is
refreshed every five seconds. Hidden tabs pause polling and connection failures
back off. Reports are fetched on demand after an attempt finishes.

**Agent library** provides persistent create/edit operations. Edits require an
expected revision; every admitted attempt freezes the selected definition,
additional instructions, runtime executable, and digest. New custom agents become
available to subsequent goals. A running graph retains its original allowed agent
IDs, while new assignments resolve the latest published revision of those IDs.

`POST /api/graphs/automatic` accepts `{key, project_id, goal}`. Creation deduplicates
before runtime checks. `POST /api/graphs/{id}/automatic` accepts `{key, revision}`
to explicitly start an untouched old goal. Merely opening an old graph never starts
it. `GET /api/graphs/agents` reports runtime readiness and library summaries.
`GET /api/agent-library` reads definitions; `POST /api/agent-library` publishes a
worker revision with `{key, id, revision, name, description, instructions}` (empty
`id` creates an agent). The built-in control planner is not editable through this API.

Goal limits are 64 tasks, 96 attempts, 24 planner calls, two retries per worker and
a two-hour deadline. Rejected planner proposals get at most two repair calls using
the rejection reason. Runtime/authentication failures remain visible for inspection;
the system does not repeatedly retry invalid credentials. Stopping uses process-group
cancellation and preserves harvested output. Server restart parks interrupted runs;
explicit retry/replanning is available after recovery.

The implementation currently supports the installed Codex CLI. No API key is copied
into graph records, prompts or library definitions. Native configuration is resolved
at launch. This remains trusted local execution with Codex's read-only/workspace-write
sandbox settings; Mitos does not advertise strong provider confinement. It does not
publish, merge or write changes back to the original project automatically.

Lost mutation responses retain their original idempotency key for reconciliation,
including after reload. Existing deterministic graph APIs remain available for
integrations, but manual graph editing is no longer part of the normal Work page.

## Registered controller integrations

Advanced API clients can still register deterministic controller applications with
`graph_controller: true` in `app.json`. These receive `graph.context` on stdin and
write `graph-proposal.json` as described below. They are independent of the built-in
automatic Codex planner and are not required to use the product.

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
mechanism. The automatic planner prioritizes worker reports and receives saved
patches and manifests for unaccepted results when they fit the dependency limits
(each supplementary file is capped at 1 MiB). It inspects this evidence before
requesting further review. Workers never need to know this controller protocol.

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
