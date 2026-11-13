# Independent supervision

Open **Project setup → Autonomy & approvals → Independent supervision**. Choose
explicit rules or an independent model, select a configured decision application,
enter project boundaries and set the supervisor's per-session call allowance.
The model uses the existing Decisions API adapter, with separate instructions,
inputs, receipts and budget from request classification and answer selection.

The coordinator plans work; workers execute it; the router chooses who should
answer a question; the supervisor checks boundaries. The daemon owns the lifecycle.
Coordination uses `/api/projects/{project}/coordination`, `desk coordination`,
`coordinator_id` and `MITOS_COORDINATOR_ID`. The old coordination `/supervision`
endpoint and CLI command are removed without aliases. Saved execution identifiers
such as `mitos.planner` remain identities; they are not supervision roles. A supervisor's `allow` verdict does not grant permission,
answer a question, accept a task result or bypass a command rule.

Explicit deny rules are checked before intercepted approvals can be answered.
Optional workspace checks resolve existing path ancestors (including symlinks),
check reported working directories and file changes, and flag expanded filesystem
permissions. In model mode, native requests require a current-policy clearance
before either a person or automation can resolve them. Codex command, file-change
and MCP-tool lifecycle events also receive independent review. Duplicate evidence
reuses its receipt; changing policy does not reset the per-session call allowance.
Model failures, exhausted budgets and insufficient confidence hold work.

A non-allow verdict and incident are committed together. The project hold blocks
queued admission, graph/coordinator progress and native session launches. Stop
intent records process targets before cancellation; active automatic work, owned
sessions, project-analysis processes and managed builds receive stop requests. Bridge capabilities are revoked.
Restart retains the hold and retries incomplete stop intent. The project page
shows the evidence, stop targets and whether termination was confirmed. Updating
settings or disabling supervision does not dismiss an existing incident.

Resolve an incident on the project page after inspecting its evidence and correcting
the cause. Resolution requires an operator note and explicit confirmation that
outstanding processes stopped; known live owned work must finish stopping first.
Resolving releases admission for future work. It does not restart stopped sessions.
External bridge processes and interrupted processes whose custody was lost require
operator inspection; MITOS does not report their termination as confirmed.

## Coverage and limits

* Supported native approval requests can be intercepted before their dependent
  action. Structured Codex asynchronous questions remain nonblocking.
* Codex tool lifecycle notifications are observations: the underlying action may
  already have started. Review pauses event forwarding, not every native action.
* Claude's installed bridge hooks cover the supported native approval/tool requests;
  there is no claim of equivalent Codex lifecycle-event coverage.
* Native terminals without a bridge can be stopped as owned processes but have no
  structured action review. Direct external CLI sessions bind to a uniquely matching
  project directory; their process termination cannot be confirmed by MITOS.
* Exact deny rules are not a shell semantics analyzer. A changed command spelling,
  implicit access by a tool, omitted events, subprocesses escaping process groups,
  and concurrent symlink changes are outside this monitoring guarantee. Same-user
  execution is not hostile-agent confinement. Strong prevention requires an enforcing
  execution substrate; project guidance and model review cannot supply that boundary.

The supervision API is `GET /api/projects/{project}/supervision/incidents` and operator
`POST /api/projects/{project}/supervision/incidents/{incident}/resolve` with `note` and
`confirmed_stopped`. Authenticated bridges submit native observations to
`POST /api/agent-sessions/{session}/observe`. Policy changes use the existing
revision-checked project agent-policy endpoint.
