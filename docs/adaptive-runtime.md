# Adaptive work graphs

The backend composes existing Tasks and Run attempts into durable, mutable work
graphs. An operator, registered orchestrator application, or deterministic client can
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
2. A read-only orchestrator chooses agents from the library and proposes assignments,
   dependencies, retries or replacements. A task-specific role can specialize a
   base definition without changing invocation, policy or permissions.
3. The scheduler admits real Codex attempts into its existing two-slot work pool.
   Workers run in fresh workspace copies. Bound upstream reports and patches are
   supplied as input; clean patches are applied before launch. Conflicting patches
   are reported to the worker for reconciliation. Originals remain untouched.
4. Workers return ordinary text. Reports, logs, changes and workspace archives are
   saved separately. The orchestrator reads these artifacts, evaluates the goal and
   criteria, and either extends the graph or accepts the evidenced result. Worker
   exit status is not a verifier verdict. Completion is labelled agent-reviewed.

The project has stable **Conversation**, **Workflow**, **Result** and **Changes**
views. Conversation is the default entry and shows the request, progress and answer
preview. Result is a dedicated view; Changes compares the project checkout or a
selected saved agent patch without entering the older native-session interface.
**Automations** configures repeated plugin jobs (not adaptive graph occurrences);
**Project setup** holds project context, device information and session limits.

The orchestrator chooses a presentation suited to the request: narrative, checklist,
table, saved files/diffs and labelled links to actual issues or pull requests.
`result.items` contains bounded `file`, `changes` or `link` items; source-bound file
items and HTTP links are validated, and the UI never executes generated HTML.
Ordinary worker output remains unconstrained prose. Formatting the final answer
uses the existing orchestrator rather than requiring another review task.

After completion, the message composer starts a continuation with `from_graph` and
`from_revision`. The creation key deduplicates uncertain retries. The predecessor
stays settled; the continuation gets fresh bounded work allowances and freezes the
prior answer and its accepted source runs. Orchestrator and worker attempts receive
checksum-verified reports, manifests, patches and archives in a separate read-only
input directory. Old patches are not implicitly applied. External publication must
be expressly requested by the current user; saved output cannot authorize it.
Issue/PR creation still needs available native tools, credentials and network access.
Active work currently exposes progress and stop controls; the follow-up composer
sends once an answer is ready, rather than implying that messages steer an already
running native attempt.

The left work tree selects goals and agents. The goal remains the page title; a
single Orchestrator card anchors the graph and connects to worker assignments.
Selecting it opens activity, planning rounds, the original request and final result.
One Orchestrator entry in the tree groups planning rounds into a chronological
history; arrows between assignments show their dependencies. Replaced assignments are available through **Show replaced work**.
The graph also supports zoom, collapse and a list view. Selecting an agent opens its output in a wide reader, which can
expand to the full content area. Orchestrator proposals become readable summaries and
expandable assignments; raw data is available separately. Attempt history opens
saved output and files in place, with technical execution pages under Diagnostics.
**View result** opens the goal's saved final answer, separately from orchestrator
acceptance notes and worker reviews. Automatic completion requires a bounded
`result` with a title, Markdown answer and accepted worker `source_tasks`; the
runtime freezes those sources to their accepted run IDs. The orchestrator synthesizes
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
Native graph agents claim paths in their individual workspace copies, so independent
audits can execute concurrently. Named resource leases still conflict across copies;
dependencies still order assignments that consume earlier results. Other application
executors retain their existing source-workspace claim semantics. Native agents also
receive a per-attempt writable Go build cache in the temporary directory, removed
after execution and harvesting, so cache access does not require sandbox escalation.
Only the selected graph is polled each second; summary status is
refreshed every five seconds. Hidden tabs pause polling and connection failures
back off. Reports are fetched on demand after an attempt finishes.

**Agent library** provides persistent create/edit operations. Edits require an
expected revision; every admitted attempt freezes the selected definition,
additional instructions, runtime executable, and digest. New custom agents become
available to subsequent goals. A running graph retains its original allowed agent
IDs, while new assignments resolve the latest published revision of those IDs.

`POST /api/graphs/automatic` accepts `{key, project_id, goal, execution?}`. Creation deduplicates
before runtime checks. `POST /api/graphs/{id}/automatic` accepts `{key, revision}`
to explicitly start an untouched old goal. Merely opening an old graph never starts
it. `GET /api/graphs/agents` reports runtime readiness and library summaries.
`GET /api/agent-library` reads definitions; `POST /api/agent-library` publishes a
worker revision with `{key, id, revision, name, description, instructions}` (empty
`id` creates an agent). The built-in control orchestrator is not editable through this API.
An edit keeps the prior manifest (runtime requirements, capabilities, context policy,
limits, provenance) and changes only those three fields; a new agent derives its manifest
from `mitos.generalist`. Task-specific specializations are bounded by the base
definition's `specialization.allow` and `max_instruction_bytes`, which the library
summary exposes as `max_specialization_bytes`.

### Task traits, harnesses and model tiers

Where a worker runs depends on four independent things:

- **Role**: which agent definition does the work (Engineer, Architect…). It carries
  instructions, context policy and authority.
- **Nature**: what kind of work the task is: `context`, `design`, `implement`,
  `document`, `review` or `verify`. Each worker definition declares the natures it
  supports and a default (`natures` in `agent-definition/v2`); a task may choose
  only a supported nature.
- **Size**: how much work: `small`, `medium` or `large`. It sets the attempt's time
  and turn budget.
- **Intelligence required**: how much reasoning capability the work needs: `low`,
  `medium` or `high`. It selects the model tier. Size and intelligence vary
  independently: a mechanical rename across many files is large and low; a subtle
  ten-line concurrency bug is small and high. Domain knowledge is not intelligence;
  that belongs to role, skills and knowledge.

Whoever requests a task declares its **traits**: the orchestrator in an `add`
operation, or a worker in a proposal (same `GraphSpec` shape):

```json
"traits": {"nature": "", "size": "large", "intelligence": "low"}
```

`size` and `intelligence` are required for native agent tasks; an empty `nature`
takes the role's default. Requesters never choose harnesses or models. The task's
`brief.timeout_minutes` is derived from its size. A task may set `harness` to a
permitted worker harness as an explicit override; models are never named in tasks.

**Nature narrows authority.** `context` and `review` work runs without
`workspace.write`, whatever the role may be granted; the authority receipt records
the nature and the withheld capability. Such tasks may propose follow-up work but
cannot yield on a blocking proposal.

The execution policy (`agent-execution/v2`) separates the two decisions a former
preset bundled:

```json
{
  "version": "agent-execution/v2",
  "harnesses": [
    {"id":"code","name":"Implementation","runtime":"codex","profile_id":"",
     "tiers":{"low":{"model":"","effort":"low"},"medium":{"model":"","effort":"medium"},"high":{"model":"","effort":"xhigh"}}},
    {"id":"design","name":"Design","runtime":"claude","profile_id":"",
     "tiers":{"low":{"model":"haiku","effort":"low"},"medium":{"model":"sonnet","effort":"medium"},"high":{"model":"opus","effort":"high"}}}
  ],
  "harness_rules": [
    {"match":{"role":"mitos.architect"},"harness":"design"},
    {"match":{"nature":"design"},"harness":"design"},
    {"match":{"role":"mitos.architect","nature":"review"},"harness":"code"}
  ],
  "default_harness": "code",
  "allowed_harnesses": ["code","design"],
  "orchestrator": {"harness":"code","tier":"high"},
  "size_budgets": {"small":{"minutes":15,"max_turns":40},"medium":{"minutes":30,"max_turns":80},"large":{"minutes":60,"max_turns":160}},
  "context_floor": {"large":"medium"}
}
```

1. **Harness** (an installed coordinated coding agent and its environment profile). Harnesses
   that cannot run the task are filtered out first: the definition and nature must be
   admissible on the runtime under the project's enforcement policy (for example,
   `strict_required` with a required `workspace.write` excludes Claude, whose
   restrictions are advisory locally). Among the rest, the most specific matching
   `harness_rules` entry wins (role and nature beat either alone). Validation
   refuses equal-specificity rules that overlap without a more specific rule for the
   overlap, so rule order never decides. Without a match the `default_harness` is
   used; if nothing eligible remains, the task is refused with the reason.
2. **Model tier**. Within the chosen harness, `intelligence` selects the tier: a
   model and a reasoning effort (Codex `model_reasoning_effort`: minimal…xhigh;
   Claude `--effort`: low…max). A size's `context_floor` may raise the tier for a
   larger context window, never lower it. An effort the harness cannot honour makes
   the policy invalid at validation, not at launch.

Size budgets set `brief.timeout_minutes` and cap the definition's `max_turns`; the
definition's own limits and the graph's allowances still bound them.

The `execution` object is accepted in personal/project runtime preferences, the
project instructions block, automatic goals and scheduled goals. A project policy
replaces the inherited policy as a whole; a launch policy replaces the project
policy as a whole. Role-only preset policies (`presets`, `roles`,
`allowed_workers`) are refused, not translated: redefine harnesses, tiers and
rules. **Project setup → Coordinated work agents** edits the policy; **Start
coordinated work** edits it for one launch.

Admission refuses unknown roles, natures and harnesses, unsupported runtimes,
efforts and unavailable profiles/executables. Model names are checked by the native
provider at execution. Each graph freezes the policy; each admitted assignment
freezes a `selection`: harness and the rule (or default/override) that chose it,
tier and the trait (or floor/escalation) that chose it, the declared traits and the
budget, plus runtime, model, effort, executable and profile configuration
(environment **variable names**, never credential values). Assignment details show
each decision and its reason. A changed executable/profile configuration is
refused; credentials are resolved anew. Follow-ups inherit the previous graph's
policy unless explicitly overridden.

**Escalation on retry.** A `retry` may set `escalate`:

- `size` when the attempt ran out of its time or turn budget: one size up;
- `intelligence` when its work fell short on quality: one tier up within the same
  harness.

The escalated retry continues the previous attempt's native conversation
(`continue_from`, with an `escalation` continuation) on the larger budget or
stronger model; conversation continuity is kept because only tier, effort and
budget change. The harness never changes automatically, since a native
conversation is tied to it; moving work to another harness is an explicit new
assignment. Escalations are bounded by the retry allowance, only go up, and are
recorded on the task.

**Calibration.** `GET /api/projects/{project}/execution-calibration` groups settled
native worker attempts by requester (`operator`, `orchestrator`, or
`proposal:<role>` for work admitted for a worker's proposal), nature, size,
intelligence, harness and tier. Each group reports attempts, finished and accepted
counts, budget exhaustion, size and intelligence escalations, the budget, and
median minutes and turns. Use it to see whether planners' estimates are calibrated
and to tune the policy from evidence.

Claude uses the bidirectional stream-json control protocol and an SDK MCP server
over the same process pipes for the granted handoff tools. Structured orchestrator
results, registered worker artifacts, approvals and questions use the existing
Mitos contracts. Native delegation tools are disabled: Mitos schedules the workers.
Only Mitos's handoff MCP server is connected in this adapter; other native MCP
connections are not imported. Codex retains its app-server transport. Pi and other
session adapters do not yet support coordinated work and are refused here.

Native delegation is removed from the tool surface of both harnesses, not
discouraged by instructions. Claude workers launch with an explicit `--tools`
allowlist, `--disallowedTools Agent,Task` and `--setting-sources ""`. Mitos-owned
Codex launches (workers, structured stages and read-only investigations) pass
`-c features.multi_agent=false -c features.multi_agent_v2=false` and run under a
Mitos-owned `CODEX_HOME` at `<store>/native/codex/<hash of source home>/`. The
operator's `config.toml`, feature flags, skills, plugins, rules and memories do
not reach that home. Only `auth.json` passes through, as a symlink, so token
refreshes write back to the operator's file. The home is stable per source home
(the profile's `CODEX_HOME`, else `~/.codex`) because saved native threads live
there and continuations resume them. Credentials in the environment
(`OPENAI_API_KEY`, `CODEX_API_KEY`, `OPENAI_BASE_URL`) pass through unchanged.
Keyring-stored Codex credentials and custom providers that exist only in the
operator's `config.toml` are not available to workers. Interactive desk sessions
keep the operator's own harness settings.

Claude's native tool restrictions do **not** provide OS confinement on the local
provider. Its workspace, command and network capability receipts are advisory;
strict policies refuse incompatible definitions. The adapter preserves native
credentials but does not inherit user/project permission settings that could
bypass Mitos request handling. Tests use local wire fixtures, including both
orchestrator harnesses; they do not call paid model providers. Protocol references:
[Claude CLI](https://code.claude.com/docs/en/cli-reference) and the official
[SDK control implementation](https://github.com/anthropics/claude-agent-sdk-python/blob/main/src/claude_agent_sdk/_internal/query.py).

Before launch, each agent attempt resolves its **authority receipt** (`authority.json`):
the definition's requested capabilities intersected with what the runtime can provide,
with each capability marked `enforced` (the runtime bounds the grant or blocks the
withheld capability), `advisory` (it cannot) or `refused`. Missing runtime features and
unsupported required capabilities always refuse the attempt as a setup prerequisite.
The `enforcement` setting (personal defaults, overridable in the project's `mitos`
settings block) selects what else refuses: `advisory` (default) refuses nothing more,
`strict_required` refuses advisory required capabilities and `strict` refuses any
advisory capability. With Codex on local processes, reads are not confined, command
execution cannot be disabled and granted network access is not read-only, so
`strict_required` refuses `mitos.engineer` and `mitos.tester`. The receipt drives the
attempt: `workspace.write` selects the writable sandbox (otherwise read-only),
`network.read` enables sandbox network access, artifact tools are exposed and accepted
only when granted, the timeout is the smaller of the definition and attempt limits, and
`max_turns` bounds model steps (completed tool actions plus the final answer).
`GET /api/graphs/capabilities` publishes the policies and per-runtime profile.

Every agent binding freezes a **runtime selection receipt**: the installed
runtimes considered, each availability and `runtime.requires` compatibility
check, and the rationale for the applied runtime. Unsupported hard requirements
refuse the binding instead of disappearing; implicit defaults fall back to a
compatible installed runtime, while presets, role rules and task selections are
explicit pins that never silently switch. Multi-runtime negotiation beyond
Codex/Claude discovery, scope-scoped discovery, lifecycle operations and the
skill/knowledge registry remain unimplemented (`complete_profile` stays false).

Each attempt's prompt is assembled under its definition's `context` policy and
recorded in a **context receipt** (`context.json`): every input's kind, source,
character count, SHA-256 and whether it was included, omitted or not requested.
Protocol text and definition instructions come first and are outside the budget;
inputs follow in stability tiers (graph, then task, then attempt facts such as run
IDs and deadlines) so consecutive attempts share the longest byte-identical prefix
for provider prompt caching. Inputs the definition does not declare are excluded.
Required inputs are never dropped: a missing or over-budget required input refuses
the attempt as a setup prerequisite. On overflow, optional inputs are omitted
largest first; each is written in full under the attempt's `context/` directory and
replaced by a reference to that file and its digest. `summarize_optional` is applied
the same way, since a generated summary would change the prompt bytes on every
attempt; `wait` refuses instead. The orchestrator's planning context is split into
`agent_library`, `graph_state` and `remaining_allowance`; `graph_state` is an
append-only journal of immutable task-fact entries in commit order, compacted
only at a fixed entry boundary, and the context receipt records the byte prefix
each planning call shares with the previous one. Task detail (full specs,
criteria, artifact lists) is paged on demand through the `graph_read` handoff
tool instead of being inlined. The kernel bounds the automatic planning payload
at 64 KiB, well under the orchestrator definition's context budget.

Goal limits are 64 tasks, 96 attempts, 24 orchestrator calls, two retries per worker and
a two-hour deadline. Rejected orchestrator proposals get at most two repair calls using
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

## Project instructions and execution preferences

Project setup opens the repository's `AGENTS.md` (or existing lowercase `agents.md`)
as one readable document, with a setup conversation alongside it. The user describes
changes; the assistant asks focused questions or proposes the complete revised file.
The user can compare saved/proposed versions, refine the proposal, and apply it.
The file is edited directly through the shared Edit/Preview/Split component. Saving is a normal
working-tree edit, never a commit, push or GitHub action. Existing file hashes reject
stale proposals; symlinks, special files and ambiguous duplicate names are refused.
The old section-editing API remains compatible, but the page no longer requires a
Mitos-specific section structure.

The assistant uses installed runtime discovery. Codex, Claude Code and Pi have
read-only setup adapters; other installed runtimes are listed with their actual
capability. Setup runtime/model selection is independent of automatic task execution,
which supports all built-in coding agents. A setup reply requires operator review before saving.

Coordinated work uses native protocols for Codex and Claude Code. OpenCode, Pi,
Gemini CLI, GitHub Copilot, Aider, Goose, Amp, Cursor Agent, Qwen Code, Cline
and Kilo Code use their non-interactive CLI. Each invocation can submit a Mitos
tool request, ask for an operator decision or return a final answer. Mitos passes
the receipt into the next invocation in the same workspace. Worker results and
orchestrator plans go through the same validation as native-protocol results.
The turn budget counts CLI invocations. A CLI can take multiple internal steps
during one invocation. On the local process provider, native tools follow each
CLI's own permissions. Strict policies refuse capabilities the provider cannot
enforce. Confined print clients need model credentials available inside the
sandbox, such as an API key supplied by the selected environment profile.
Conversations and pending proposals persist in the server data directory, survive a
browser reload, and appear interrupted after a server restart. They have bounded
history/output, a cancellation endpoint and an eight-minute reply timeout.

“My defaults” is separate from the project file and supports the same conversational
proposal/review flow. Personal guidance, runtime/model and numeric defaults are
stored in the server's `preferences.json`, never in the repository. The project setup
assistant does not receive personal guidance; the personal-default assistant receives
no project document and runs outside the project directory.

An optional fenced `mitos` block in AGENTS.md contains deterministic execution
settings, for example:

```mitos
{"runtime":"codex","model":"","minutes":120,"attempts":96}
```

Only these explicit structured values affect execution. The parser rejects unknown
keys, unsupported runtimes, malformed/duplicate blocks and out-of-range values.
Minutes may be 1–43200; attempts 1–2048. Missing/zero numeric values and blank strings
inherit earlier local project overrides, then personal defaults, then the native model,
120 minutes and 96 attempts. The page exposes effective values and notes legacy
local overrides. Markdown risk labels, Git/review policy and monetary budgets remain
instructions, not spending enforcement or permission to publish. The assistant must
not invent numeric limits from qualitative descriptions.

New automatic goals, continuations and explicitly started old goals freeze the
effective setup. The full project document and personal guidance enter the orchestrator's
prompt. Every attempt receives the same root instructions in its workspace baseline,
alongside normal nested instruction files; later edits do not change existing work.
If no file exists, legacy saved context/Git guidance seeds a draft until the user saves.

`GET /api/projects/{project}/setup` returns the document, file hash and effective
settings. `PUT /api/projects/{project}/instructions` accepts `file_hash` and either
`document` or legacy `sections`. Personal preferences use `GET/PUT /api/preferences`
with a revision check. Legacy overrides use `PUT /api/projects/{project}/preferences`.
Setup conversation endpoints are `GET/POST /api/projects/{project}/setup/conversation`
and `POST .../conversation/stop`; their target is `project` or `defaults`. POST requests
include an idempotency key, runtime, optional model and message. The latest request
can be replayed without launching another process. Settings saves require the file
hash or preferences revision captured when the proposal was prepared.

Markdown documents use the shared light-DOM `<mitos-markdown>` component.
`MitosMarkdown.view(text, {filename, link})` creates a read-only document with
Preview/Source controls; omitting `filename` uses a compact view for conversation
fragments. `MitosMarkdown.editor(text, {filename})` adds Edit/Preview/Split modes,
formatting tools, soft wrapping, aligned line numbers and cursor/dirty status.
Source and preview panes have equal fixed heights and cannot be resized independently. Its `value` property sets
loaded content; native textarea input events bubble normally. A handled, cancelable
`markdown-save` event connects Ctrl/Cmd+S to the owning page's save action.

Existing forms opt in with `textarea[data-markdown="Instructions.md"]`. Enhancement
retains the same textarea, name, ID, constraints and native undo history. Form reset
and dialog opening synchronize externally loaded values. The component uses the
same safe Markdown parser for all previews; it never evaluates authored HTML.
File-link callbacks remain attached when switching modes. JSON editors, terminal
input and short chat composers remain plain inputs rather than Markdown documents.

## Registered orchestrator integrations

Native agents and registered applications use one `OrchestratorPlan` contract:
`operations`, `accept`, `complete`, `result`, and `explanation`. Scheduling uses
the same `GraphSpec.brief.scheduling` and `GraphOperation` types in every adapter;
there is no separate `steps`/`after`/`retry` shorthand. Discover the exact native
structured-output schema at `GET /api/graphs/orchestrator-schema` and supported
sources, guards, and joins at `GET /api/graphs/capabilities`.

`operations` contains graph amendments such as `add`, `dependencies`, `retry`, and
`supersede`. An `add` carries the full task `spec`; a native agent uses an
`app_id` such as `agent:mitos.engineer`. `accept` contains observed worker task IDs
and evidence-based rationales. Automatic graphs permit this agent review and
`complete` with a final `result`; operator-reviewed integrations still require
operator authority to accept work. A contract does not grant new authority.

For example, put the following inside a consumer's `brief.scheduling` to wait
for two matching sources. References can be task IDs or proposal-local task IDs:

```json
{
  "deps": [
    {"from": "implementation", "guard": "candidate_available", "payload": "data", "artifacts": ["report.md", "changes.patch"]},
    {"from": "audit", "guard": "done", "payload": "control"}
  ],
  "join": {"rule": "all"}
}
```

No dependencies means independent work. `all` waits for every hard source;
`any` waits for one; `k_of_n` with `k` waits for that many. Race/quorum witnesses
are selected deterministically by journal sequence, then source ID. Other work
is not cancelled, and required obligations still need acceptance. Soft sources
never block readiness. Relations are finish-to-start (`FS`) only.

Task guards are `candidate_available` (successful settled output, not yet
necessarily accepted), `done` (accepted result), `failed`, and `cancelled`.
Guard mismatches remain waiting; they are not automatically skipped or counted
as accepted work. Recovery uses explicit retries, replacements, or amendments.
Content-based decisions use an orchestration round that reads evidence and adds
the selected branch; there is no executable predicate language. Automatic
orchestration currently waits for running/ready workers to settle before its
next round. Predeclared downstream tasks can start independently of that round.

### MQTT inbox sources

An inbox is a dependency source; `inbox_received` is its guard. It participates
in exactly the same all/any/quorum join as task outcomes. For example:

```json
{
  "from": "release-signal",
  "relation": "FS",
  "guard": "inbox_received",
  "strength": "hard",
  "payload": "data",
  "inbox": {
    "project": "PROJECT_ID",
    "mailbox": "GRAPH_ID",
    "sender": "PEER_ID",
    "kind": "event",
    "type": "workflow.signal",
    "correlation": "release-v1",
    "generation": 1,
    "after_sequence": 0
  }
}
```

`from` is a unique local source name for an inbox dependency. The orchestrator
receives the current graph inbox address, generation, and journal cursor in its
context. A zero cursor freezes the current journal position when the dependency
is committed, so old messages do not accidentally launch new work. An explicit
earlier cursor permits replay from that position. Cursors are exclusive.

Messages must first pass the existing signed-envelope and peer-grant admission
path. A graph accepts `event` / `workflow.signal` at its own project/graph mailbox
only while active, within its deadline, and for its current orchestrator generation.
Existing admitted project inbox messages can also be selected. Project, mailbox,
kind, type, and generation match exactly; nonempty sender and correlation add
exact filters. Wildcards are not evaluated. Cross-project sources are rejected.
The topic is `mitos/v1/DESTINATION/PROJECT_ID/GRAPH_ID/event`; declaring a workflow
dependency neither adds a broker subscription nor grants a peer permission.

Only `accepted` or `applied` inbox receipts enable work. Rejected and expired
deliveries never match. The earliest matching admitted message is bound at task
admission, including its sequence, digest and original envelope (at most 1 MiB of
inbox evidence per admission). Data dependencies
receive a checksum-verified, read-only `message.json` through the existing input
mechanism; inbox sources do not take artifact names. A control dependency only
gates execution. Redelivery is deduplicated by the inbox and cannot launch another
attempt. Bindings survive restart and do not drift to newer messages. An admitted
message remains evidence after its delivery deadline passes.

These are one-shot task activations, not consuming queue reads or recurring
subscriptions: one admitted message may enable multiple matching tasks. Waiting
uses no worker slot. A later occurrence requires a new task/graph and cursor;
retrying a task retains its original selector. Empty operations may wait for
already scheduled tasks or inbox signals without spending repeated orchestration
calls. Inbox content is evidence, never authority to change grants or instructions.

Resuming a graph or replacing its orchestrator advances unsatisfied graph-mailbox
selectors on tasks that have no queued attempt to the new generation, preserving
their cursors and other filters. A selector with an already admitted matching
message retains that evidence, including when other join sources are still
waiting. Queued/admitted attempts keep their frozen specifications and witnesses;
selectors for other mailboxes or explicitly different generations are unchanged.

### Terminology and saved-state compatibility

The role, built-in agent, UI, and API use **orchestrator** (`mitos.orchestrator`,
`orchestrator_application`, `orchestrator_calls`, `orchestrator_for`, and
`replace_orchestrator`). Historical graph fields and stored session lineage are
read through migration aliases; new writes emit the canonical names. Frozen old
agent identities remain executable. New proposals must use the shared operations
contract; the old automatic-plan shorthand is rejected rather than interpreted
with different scheduling semantics.

Advanced API clients can still register deterministic orchestrator applications with
`graph_orchestrator: true` in `app.json`. These receive `graph.context` on stdin and
write `graph-proposal.json` as described below. They are independent of the built-in
automatic Codex orchestrator and are not required to use the product.

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
- Outcome-triggered planning, fenced orchestrator replacement, optional typed model
  choice over validated candidates, and durable inference/application receipts.
- Explicit scope closure and operator root acceptance. Finishing a command, an empty
  queue, or an agent's narrative never accepts the root goal.

A task with `acceptance: application` accepts a successful result only when its
registered application verifier passes. `acceptance: operator` keeps that result
as an unaccepted candidate until explicit review. Verifier policy is trusted local
configuration, not enforcement of arbitrary natural-language criteria.

## HTTP contract

These routes use the existing localhost/operator trust boundary, cross-origin
protection, and home-server checks. Orchestrator execution is trusted local code;
this implementation does not introduce a sandbox or multi-tenant authorization.

| Method and route | Operation |
|---|---|
| `GET /api/graphs/capabilities` | Implemented adapters and limitations |
| `GET /api/graphs/applications` | Registered executor choices and orchestrator discoverability |
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
    "expansions": 16, "orchestrator_calls": 16, "depth": 8, "fanout": 16,
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
orchestrator application configured, a caller supplies amendments. Read the graph,
then POST an envelope containing `profile`, a fresh `key`, `graph_id`, `revision`,
`policy_revision`, `generation` (the current `orchestrator_generation`), `deadline`,
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
`reopen`, `cancel`, `replace_orchestrator`, `dismiss_decision`, and `accept`.
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

## Orchestrators and native output

Set `policy.orchestrator_application` to an installed application also listed in
`policy.applications`. Mitos schedules it when an expansion needs planning or a
worker outcome introduces a relevant trigger. Its stdin has the normal application
input plus `graph.context`, a JSON string containing bounded goal/task facts,
result summaries, artifact references, policy, and triggers. Settled worker output
and report artifacts are delivered through the existing immutable dependency-input
mechanism. The automatic orchestrator prioritizes worker reports and receives saved
patches and manifests for unaccepted results when they fit the dependency limits
(each supplementary file is capped at 1 MiB). It inspects this evidence before
requesting further review. Workers never need to know this orchestrator protocol.

The orchestrator may use any planning technology. It writes **`graph-proposal.json`**
in its workspace with an object containing `operations` and `explanation`. Mitos
captures this file from the harvested snapshot, waits for the orchestrator's registered
verification, and validates the proposal. Native stdout remains `output.txt` and
is never executed or parsed as control. A orchestrator's typed tool interface is
separate from its normal prose and from ordinary worker output.

The kernel closes the orchestrator's expansion and classifies the triggering cursor
in the same amendment. A orchestrator must not duplicate those operations. A stale or
malformed proposal leaves a visible review blocker. `replace_orchestrator` fences the
old generation, requests cancellation of superseded orchestrators, and rebuilds planning
from durable facts without resetting budgets or restarting existing workers.
Orchestrator capacity is shared with workers. Planning is serialized per graph; the
runtime does not hold a process slot merely to supervise children.

### Yield and continuation

Workers cannot spawn native subagents. A worker granted `work.propose` asks for
work through proposals on its terminal result
([agent-handoffs.md](agent-handoffs.md#proposals-and-yield)). This keeps two
agentic layers: as in Erlang/OTP, the worker asks its supervisor to start a child,
and graph validation is the admission controller.

**Yielded state.** When an attempt finishes with at least one blocking proposal,
the kernel records the proposals in `proposals` (keyed by kernel ID, with the
outcome trigger's sequence) in the same journal record as the trigger. It sets the
member's `yield` (`run_id` and its blocking proposal IDs) and `state: yielded`. The
run's process outcome is still `done`, so its slot and claim are released as for
any finished run. The member, however, is not a candidate: it cannot be accepted
or retried, it is not a failure, and the graph view shows it as waiting.
Non-blocking proposals are recorded the same way but leave the result reviewable
as usual.

**Proposal review.** The orchestrator sees unreviewed proposals, with their
submitted specs, on the worker's fact. It answers each with one of two operations:

- `accept_proposal` `{proposal, dependencies, rationale}`: `dependencies` (1–32)
  name the worker tasks that fulfil it, usually tasks added in the same amendment.
  For a blocking proposal, this is the **dependency amendment**: those dependencies
  are appended to the yielded task's spec (its revision advances), which ordinary
  `dependencies` amendments refuse once a task has attempts.
- `reject_proposal` `{proposal, rationale}`.

A trigger cannot be classified while its blocking proposals are unreviewed
(`proposal_review_required`), unless the proposer was cancelled or replaced. The
runtime classifies the reviewed triggers of an automatic orchestrator, so the plan
must review them in the same response. Proposed specs are suggestions; admitted
tasks pass ordinary validation, task and attempt budgets, and their own retries.
The proposer's budget is not charged.

**Continuation.** A yielded member continues once every blocking proposal is
reviewed and each task named by an accepted proposal has settled. A task settles
when it, or its replacement, is accepted, or when it is cancelled or replaced
without an accepted result (abandoned). The kernel then:

- drops dependencies on abandoned tasks and rewrites replaced ones to the
  accepted replacement;
- sets `continue_from` to the yielded run and records a `continuation` with each
  proposal's outcome, rationale, and completed and abandoned tasks;
- counts the cycle in `yields`.

The next attempt resumes the same native thread (`ResumeThread`) in a restored
copy of the yielded attempt's harvested workspace. Its prompt states the proposal
outcomes, and accepted results arrive through the dependency-inputs manifest. It
must submit a new result. Yield cycles do not consume `limits.retries`, which
counts attempts minus yields; they still count toward the graph's attempt
allowance.

Not implemented: proposals mid-run without yielding, and a separate
`explore`-style synchronous read-only delegation.

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
survive retries and orchestrator replacement; token usage is observed, not an enforced
monetary reservation. Configuring automatic orchestrator proposal application does
not implicitly enable model routing: clients explicitly request typed choices.

## Bounds and remaining integration

The local adapter permits at most 256 graphs; each graph has at most 512 members,
2,048 amendments/attempts, 256 expansions/orchestrator calls, depth 32 and fanout 128,
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
