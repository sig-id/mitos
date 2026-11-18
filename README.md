# Mitos

Mitos is an operating system for agents, under active development. A server owns
work, decisions, and execution; clients connect to direct and inspect it. The goal
is to preserve a responsibility through failed attempts, replacement agents, and
changes of client, with explicit authority and verified results.

Start with the [product specification](spec/product/01_PRODUCT.md) for the purpose,
market, and intended experience. The [specification guide](spec/README.md) explains their authority and organization. These are normative targets;
[delivery and conformance](spec/system/06_DELIVERY_AND_CONFORMANCE.md) distinguishes
current behavior from remaining work.

Today Mitos provides a local Go server, browser workspace, and CLI. Interactive
assignments and batch tasks share a canonical journal and Task/Run readers. Native
process execution and batch scheduling still have distinct adapters; enforced
resource accounting and remote placement remain delivery targets.

## Application server direction

Mitos also targets use as the headless agent runtime behind independent application
backends. Acegoat is the first design use case and remains a separate product and
repository. Applications own their users, conversations and domain rules; Mitos owns
durable work and replaceable execution through versioned APIs. Local use retains
its lightweight defaults. SQLite is the transactional runtime store. A separate TLS application listener
supports service principals and tenant-scoped headless work; S2 application crash conformance is implemented; remote worker claims remain
a delivery target. A PostgreSQL
server edition is deferred beyond the SQLite S2–S4 plan.
See the [server use case and delivery path](spec/README.md#application-server-reading-path).

## Start

Requires Go 1.26.11+, Git, and Bash on Linux. The first build downloads pinned Go
dependencies. Serving the workspace needs no Node build, database service, or CDN;
browser assets are vendored.

```sh
just serve                 # http://127.0.0.1:4646
just stop                  # stop this project's server; saved data remains
just demo 4756             # labelled examples in separate .mitos-demo data
just check                 # vet and race-enabled tests
```

Without `just`:

```sh
go run ./cmd/mitos serve --apps ./examples
go build -o bin/mitos ./cmd/mitos
go test -race ./...
```

`just serve` rebuilds and replaces the server using the same data directory.
The server runs in the foreground. Closing its browser leaves execution running;
stopping the server interrupts active local execution and retains saved state.
Restart requires reconciliation before replacement, not automatic process relaunch.
See [server and clients](spec/system/01_ARCHITECTURE.md) for the distinction
between client access, server ownership, and execution placement.

## Use the workspace

Open a project directory from **Projects** using the folder picker, then describe
your goal in **Work**. Browse folders on the server, filter the current folder, or
enter a path directly. The project name defaults to the selected folder name and
can be changed before opening.
Select **Start work**. Mitos plans the goal automatically, chooses agents from the
library, and creates specialized roles when needed. No manual steps or executor
configuration are required. Codex must be installed and signed in on the workstation.

Work appears as a tree in the left sidebar. The graph shows the goal, planning
calls, agents, dependencies, and current states. Select an agent to inspect its
assignment, formatted report, logs, changes, or attempts. The orchestrator reviews saved
evidence before completing a goal; process exit alone never accepts worker output.
Changes stay in workspace copies, with patches and complete workspace artifacts.
Older untouched goals require **Start automatic work**; viewing them does not launch
anything. See [automatic workflow](docs/adaptive-runtime.md#automatic-workflow).

Use **Start agent session** to select an automatically detected coding CLI and
environment profile, then enter an assignment. Codex and Claude sessions route
supported questions and approvals to **Inbox**. Other agents, including Pi and
OpenCode, open native sessions with questions and approvals handled in the browser
terminal; the picker explains this before launch. Installed agents whose adapters
cannot receive an initial assignment appear disabled with an explanation.
The CLI equivalent is `./bin/mitos agent codex --workspace /path/to/project` while
Mitos is serving. Add `--keep-open --prompt "Assignment"` to supply the initial
assignment and keep the conversation open. Codex supports live turn steering and asynchronous clarification replies.
Independent boundary review is configured in **Project setup → Autonomy & approvals**;
[supervision incidents](docs/independent-supervision.md) appear on the project page.
Bridge resume/fork is not yet supported; ordinary native sessions remain available below.

Installed interactive agent terminals are separate executors. **Sessions & earlier
work → New native session** retains their assignment, coordination inbox, reports,
and replacement workflow. Submitted session reports require explicit human review;
that acceptance covers the saved report, not unverified repository changes.

**Services** activates project applications on request or on an interval. **Resources**
shows workspace and execution information. **Inbox** collects pending report reviews,
process inspections, and batch decisions. **Devices** shows where work runs. **Agent library** lets you create and edit worker
roles; revisions are saved and running attempts retain their original definitions.
**Plugins** lists installed command tools. Run history and scheduler details are
under **Diagnostics**.

**Context & setup** retains project analysis and profile editing. Terminals retain
input, resize, scrollback, interrupt, stop, resume, fork, and handover. Earlier
sessions without task bindings remain accessible as saved history. Topics support
discussion and discovery; they do not confer execution authority.

For batch work, open **Task history → New work → Workspace inventory** and select
a local directory. This example copies eligible files, inventories their hashes,
checks the result, and produces artifacts. It makes no model calls. The optional
Workspace note example runs a real command and a separate acceptance command.

Services activate tasks through schedules, events, and other declared policies.
[Services and events](spec/system/03_ORCHESTRATION.md) covers their composition,
retries, health, overlap, and lifecycle in one contract.

## Local applications

Start from [the runnable example](examples/local-app/app.json). Applications declare
an entrypoint, command, and optional acceptance and execution requirements:

```json
{
  "id": "my-application",
  "name": "My application",
  "description": "The outcome this application produces.",
  "entrypoint": "Run the application",
  "command": ["/absolute/path/to/runner"],
  "acceptance": {
    "cmd": ["/absolute/path/to/verifier"],
    "oracle_paths": ["tests/acceptance"],
    "timeout_ms": 60000
  }
}
```

The runner receives its run identity, brief, and copied workspace through JSON on
stdin. Mitos preserves output and attempts to harvest changes after success,
failure, cancellation, or timeout. Acceptance uses saved verification material and
a separately reconstructed result. See [applications](spec/system/05_INTERFACES_AND_INTEGRATIONS.md) and
[workspaces and verification](spec/system/04_STATE_AND_VERIFICATION.md).

## Adaptive work graphs

The [adaptive runtime](docs/adaptive-runtime.md) supports dynamically added
work, atomic graph amendments, automatic application dispatch, registered orchestrator
applications, and optional typed decisions. Native worker output stays intact.
Every `/api` request sends `Mitos-API-Version: 1`; see
[application API](docs/application-api.md), which also covers headless structured
work with typed results.

The project **Work** page creates and displays these graphs. `/api/graphs` provides
the programmatic interface; `/api/graphs/capabilities` reports the implemented
adapter and remaining integrations. Native PTY sessions and services retain their
separate lifecycle.

Work is grouped beneath its project in the sidebar. Each work can be expanded
independently; selecting its title opens its conversation. **Open** is the default
list, while completed and stopped work moves to **Closed**. The work actions menu
can close, archive, restore, or reopen work. Closing or archiving active work also
requests cancellation of its agents. Reopening restores the item to Open without
restarting execution; results and earlier conversation remain available.

Work organization is persisted separately from execution status. List a project’s
work with `GET /api/graphs?project_id=…&state=open` (also `closed`, `archived`, or
`all`; omitting `state` retains the full API listing). Update an item with
`POST /api/graphs/{id}/work-state` and a JSON body containing `key`, `revision`, and
`state`. State edits use revision checks and replay-safe request keys.

## Agent library

The [default library](agents/README.md) ships eight adaptable definitions ported from
Chief Wiggum. The native Codex adapter loads these definitions, freezes them for each
attempt, and supports graph-scoped specializations. Custom worker definitions can be
added or edited in **Agent library**; subsequent goals make them available to the
orchestrator. The full [agent-library specification](spec/system/07_AGENT_LIBRARY.md)
additionally describes future runtime negotiation, knowledge and publication features.

## System Configuration

Open **System Configuration** in the sidebar (`/settings`) to configure the shared
Decisions API connection and native agent launch defaults. Select a project in
**Configuration scope** to override the connection or an agent's defaults. Existing
project connections remain overrides; projects without one inherit the system
connection. Approval boundaries and delegation remain in **Project setup**.

Provider buttons prefill the endpoint, protocol, and dedicated decision model for
TypeSafe Jev, OpenAI, Gemini, and OpenRouter-hosted decision models. All fields stay
editable. These use typed decision APIs, not generic chat completions.

The Decisions API form accepts a full endpoint, protocol, model, timeout, call
limit, and confidence thresholds. Under **Authentication**, choose **API key** to
paste your key directly, or **Environment variable** to enter a name such as
`OPENAI_API_KEY`. Pasted keys work immediately, without a restart. Environment
variables must be set before starting Mitos; restart after changing their values.
**Test API connection** makes one request without saving. Saving stores pasted keys
in owner-only files under `<data-directory>/credentials/`; settings, event logs,
and execution plans contain only references. The form never returns a saved key.
Leave its field blank to keep it, paste a replacement to rotate it, or select
**Remove saved key** and save to stop using it. Old key files remain available to
existing execution plans; they are plaintext files protected by filesystem permissions,
not encrypted. Saved keys are bound to their endpoint; re-enter the key if it changes.
For native agents, choose an existing environment profile and additional arguments
as a JSON array. Resumes and forks retain the source environment profile while using
current default arguments. Managed coordinated workers and read-only assistants
retain their separate launch protocols.

Settings are journaled in `<data-directory>/runtime.db` (normally
`.mitos/runtime.db`), outside project instruction files. See
[configuration and implementation details](docs/agent-decisions.md#system-configuration-and-project-overrides).

## Optional decision models

Command applications can add `decision_model` to choose whether to start a segment
or take an already eligible recovery. Mitos supports the Jev/Laya System One HTTP
format, OpenAI Decisions, Gemini Decisions, and OpenRouter Decisions through separate
adapters. Configure the full endpoint,
model, credential environment-variable name if needed, hooks, question, timeout,
call limit, and probability/confidence thresholds in `app.json`.

See the [configuration example and provider contract](spec/system/05_INTERFACES_AND_INTEGRATIONS.md#decision-model-provider-profile)
and the [runner runtime](spec/system/02_RUNTIME.md#runner-runtime-and-optional-decision-inference).
No model is required. A model selects from runtime-supplied choices; Mitos retains
control of commands, verification, recovery limits, and human approvals. Uncertain
or unavailable inference parks work for review. Requests and validated answers are
saved on the Run; restart does not silently repeat a possibly charged call.

## Operate and inspect

The CLI connects to the running server:

```sh
./bin/mitos run --title 'Inspect this workspace' --workspace . --minutes 5
./bin/mitos status local_RUN_ID
./bin/mitos decisions
./bin/mitos resolve --choice cancel --note 'Keep the work.' local_RUN_ID decision_ID
./bin/mitos checkpoints local_RUN_ID
./bin/mitos autonomy
./bin/mitos interrupts
./bin/mitos costs
```

Work and interactive metadata are reconstructed from the hash-chained journal in
`.mitos/runtime.db`. SQLite uses WAL and full synchronous commits. The first start
imports any `events.jsonl` and `agent-library.json` once, verifies their integrity,
and preserves the original files as backups. Subsequent starts use SQLite only. On first
startup with this model, the server imports `desk/state.json` and retains that file
as the pre-migration backup; it is no longer updated. Keep backups of the complete
data directory, including terminal output and batch artifacts. Interrupted
work retains output, partial changes, and recovery decisions. A retry gets a new
Run ID under the same Task. Silence does not authorize a pending local decision.

The local provider runs as the operator and can access the host. Batch environment
hygiene and a separate verification directory do not establish confinement. Strict
unsupported compute requirements are refused. Interactive agents use their native
credentials and configuration; free-text project rules are instructions.

## Cost statistics

Codex and Claude Code attempts report their token usage, including the split
between uncached input, cache hits and cache writes, and Mitos journals it as
`agent_usage_recorded` evidence next to the attempt. Each recorded attempt is
priced against published provider list prices (OpenAI, Anthropic and Google,
embedded with their retrieval date in `internal/modelcost`) and folded into a
per-agent report at `/costs`, `/api/agent-costs`, or `mitos costs`. Estimates
use the cheaper cached-input rate for cache hits and the provider's write
premium for cache writes; they are telemetry about spending, not invoices,
reservations or enforced budgets, and attempts or models without usage or a
published price are reported as unpriced rather than free.

The main server binds to loopback and rejects foreign Host headers and cross-origin
mutations. The optional HTTPS device API has restricted snapshot and decision scope;
it is not the full remote-client contract. See
[API and compatibility](spec/system/05_INTERFACES_AND_INTEGRATIONS.md).

## Resource limits

Project setup shows **Runtime & resource limits** with two scopes:

- **Workflow budgets:** elapsed time (including waiting), agent attempts shared
  across orchestrators/workers/retries, and model tokens.
- **Per-agent resources:** CPU capacity and memory for an agent and its children.

Elapsed time and attempt budgets are enforced today. CPU and memory caps require
provider quota support; token caps require a metered model gateway. The current
local process/native CLI runtime cannot enforce those three caps. The UI marks
those controls unavailable, and the API rejects requests for them.

Use **My defaults** or a project `mitos` block to configure supported limits:

```mitos
{
  "resources": {
    "workflow": {
      "elapsed_minutes": {"mode": "limit", "amount": 120},
      "attempts": {"mode": "limit", "amount": 48}
    }
  }
}
```

Omitted dimensions or `mode: "inherit"` inherit from the preceding settings layer.
`mode: "unlimited"` explicitly means no cap for CPU, memory, or tokens; zero is not
a valid cap. The current runtime still requires finite elapsed-time and attempt
budgets. Legacy `minutes`/`attempts` remain compatible. New work freezes the resolved
policy; changing settings does not reset running budgets. See the
[resource policy contract](spec/system/02_RUNTIME.md#resource-policy-scopes-and-enforcement)
for units, inheritance, provider requirements, and token accounting requirements.

When work reaches its time or attempt limit, choose **Resume with more resources**
on that workflow. Set additional time, a larger cumulative attempt allowance, and
the time allowed per resumed attempt. Planning and retry allowances are available
in the same dialog. The workflow ID, assignments, dependencies, completed steps,
frozen agents, and attempt history stay intact; used resources are not refunded.

Failed or interrupted agents receive new runs under their existing tasks. Automatic
Codex agents persist their conversation IDs and resume those exact conversations,
with saved workspace changes restored into fresh copies. Patches remain cumulative
from the original baseline. Older runs without saved conversations use their saved
files and output as context. An unavailable saved conversation fails visibly rather
than silently starting another conversation. Keep native Codex session storage as
well as the Mitos data directory for native continuation.

Resumed agents receive current connector capabilities and evidence paths. Earlier
transport failures are historical observations: the agent can reassess the configured
route once, while retaining the original publication authorization and approval
requirements. Optional temporary paths from an earlier attempt may be absent.

The resume API is `POST /api/graphs/{id}/resume`, with an idempotency `key`, current
`revision`, full replacement `limits` (including a future `deadline`), and
`attempt_minutes` (1–120). An untouched goal can also set `start_automatic: true`
to start its orchestrator under the new limits. Limits can only increase, within runtime maximums.
Resume waits for stopped processes and released claims, preserves supervision holds,
and rejects completed, explicitly cancelled, or service-owned workflows. Recurring
occurrences remain controlled by their service lifecycle. Pending graph decisions
must be resolved before resuming.

## Recurring agent workflows

In the project goal composer, open **When to run** and choose **On a schedule**,
**On an external event**, or **On a schedule and external events**. Choose a
minute, hourly, daily, or weekly schedule and/or enter an event name, then save.
The workflow title comes from the goal. Schedules use your device’s local time
zone; minute and hourly intervals align to the clock. **Custom cron** supports
five-field expressions. **Advanced options** lets you change the time zone.
New workflows inherit project execution limits, including elapsed time and agent
attempts, for each occurrence. Existing workflows with explicit time caps retain
those caps. In the workflow API, omitted or zero `minutes` means inherit, not
unlimited. CPU, memory, and token caps are represented by the resource policy but are not
enforced by the current local process/native CLI runtime.
The goal text is the workflow's persistent instructions; agents implement its
procedure using the project's tools and policies. Mitos does not hard-code PR,
security-review, or merge algorithms.

For example, describe your PR monitoring procedure in the goal, including how to
recognize your security review, which statuses permit merging, and what to do when
a review is pending. Authorize the intended external actions there. Select
**Every few minutes → 5 minutes** for a check every five minutes. Tell agents to report pending work
and finish each occurrence so the next check can revisit it.

The first scheduled run starts at the next matching time. **Services → Trigger
task** runs an occurrence sooner. The server must remain running; after downtime,
the saved clock coalesces missed ticks. Cron works without an MQTT broker. Each
occurrence creates an automatic agent graph, retains its instructions and trigger
context, and exposes its result and evidence in the project. Waiting approvals and
interrupted agents keep their normal recovery behavior.

**Services** shows saved instructions, queue state and the latest graph/result.
Use **Stop service group** to stop the workflow and await its agents' shutdown;
**Resume triggers** releases the hold. Edit **Project catalog** to change its
instructions or schedule. The default is one active occurrence and one queued
occurrence, shared between cron and external events. Excess triggers are recorded
as skipped; subsequent periodic runs can reconcile current state.

An external bridge can send a signed/granted MQTT `service.event` to
`studio/events` with `{"name":"repository.changed","payload":{...}}`, matching
the workflow's saved event name. Local callers can use `mitos emit-event` or the
custom-event HTTP API. Duplicate messages retain their receipt. Trigger payloads
are supplied as untrusted context, not instructions or permission grants; the
configured agents interpret them according to your saved goal. Raw GitHub webhooks
need a bridge to the authenticated Mitos message envelope.

The composer uses `POST /api/services/workflows`:

```json
{
  "id": "periodic-work",
  "project_id": "YOUR_PROJECT_ID",
  "goal": "Follow my workflow procedure, inspect current state, and report this occurrence's result.",
  "cron": "*/5 * * * *",
  "timezone": "UTC",
  "event": "repository.changed",
  "minutes": 30,
  "expected_revision": 0
}
```

Read the current catalog revision from `GET /api/services`. Omit `event` for cron
only; omit `cron` and `timezone` for events only. Repeating an identical creation
returns the saved service; a conflicting name or stale revision is rejected.
Advanced catalogs use an `agent-workflow` work binding with `project_id`, `intent`,
and `timeout_minutes`. An `events` array on a cron, interval, or manual unit adds
event intake to its existing queue. Workflow-specific conditions belong in the
instructions; logical workflow bindings do not run command condition/health probes.

## MQTT 5 messaging

Enable messaging with `mitos serve --mqtt-config /path/to/mqtt.json`. The broker
runs separately; Mitos uses Eclipse Paho and keeps durable inbox/outbox records in
its journal. For a local broker, the minimal configuration is:

```json
{"broker":"mqtt://127.0.0.1:1883","peers":[]}
```

Use `mitos message status` to read this server's identity, public signing key, and
connection state. Remote broker URLs require `mqtts://`; configuration accepts
`ca_file`, `cert_file`/`key_file`, `username`, and `password_env` (the environment
variable's name, never the password value). Plaintext requires a loopback IP.

To authorize another server, add its verified identity/key and exact inbound grants:

```json
{
  "id":"SERVER_ID_FROM_PEER_STATUS",
  "public_key":"BASE64_PUBLIC_KEY_FROM_PEER_STATUS",
  "grants":[{"project":"studio","mailbox":"operator","kind":"note","type":"text"}]
}
```

Put that object in `peers`. Configure both ends, even when one only receives receipts;
empty grants allow no incoming work. Restart to apply changes or revoke a peer.
Broker credentials and ACLs must restrict subscriptions to
`mitos/v1/SERVER_ID/+/+/+` and publication to authorized destination namespaces.
Signing authenticates messages but does not conceal payloads from the broker.
Different brokers require an explicit, scoped bridge. Back up `mqtt-ed25519.key`
with the data directory; keep it private. Automatic key rotation is not provided.

`mitos message send --file request.json` accepts a stable `id`, destination `to`,
`project`, `mailbox`, `kind`, `type`, future RFC3339 `deadline` (within seven days),
and JSON `payload`. Reuse the same file to retry an ambiguous response. For a note,
use `kind: "note"`, `type: "text"`, and `payload: {"body":"Review the result"}`.
The operator inbox is `studio/operator`; a responsibility mailbox uses its project,
ID, and current `generation`. To trigger configured services, authorize
`studio/events` with `kind: "command"`, `type: "service.event"`, and payload
`{"name":"inventory.ready","payload":{}}`. This uses saved service policy.

```sh
mitos message outbox --id MESSAGE_ID
mitos message inbox --project studio --mailbox operator --after 0
mitos message ack --sender SERVER_ID --id MESSAGE_ID --generation 0
```

`accepted` means a note is durably stored; `applied` means a service event was
committed, not that its tasks finished. Broker acknowledgment alone changes neither.
Pending messages survive restart and broker-session loss. Retry exhaustion after a
possible send is `unknown`; investigate rather than invent a new ID. Receipts are
retained with a 10,000-record admission limit per direction. See the
[messaging contract](spec/system/05_INTERFACES_AND_INTEGRATIONS.md#mqtt-5-messaging)
for bounds and remaining capabilities.

## Repository

| Path | Contents |
|---|---|
| `cmd/mitos/` | Server and HTTP-backed CLI |
| `internal/studio/` | Canonical journal, common work readers, applications, and batch execution |
| `internal/desk/` | Interactive projects, PTY sessions, topics, and builds |
| `internal/compute/`, `internal/workpool/` | Providers, admission, scheduling, and dependencies |
| `internal/workspace/`, `internal/acceptance/` | Snapshots, artifacts, and verification |
| `internal/observation/`, `internal/autonomy/`, `internal/decision/` | Observation, recovery, and human decisions |
| `internal/modelcost/` | Published model list prices and usage-based cost estimates |
| `internal/web/` | Server-rendered browser interface and vendored assets |
| `examples/` | Runnable applications |
| `spec/` | Product, six system specifications, and ADRs |

Use `just check` for source changes. Documentation changes should preserve local
links, consecutive numbering, and the distinction between requirements and shipped
capabilities. Update the owning specification and ADR when architecture changes.

## License

Mitos is licensed under the [MIT License](LICENSE). Bundled third-party components
retain their accompanying license notices.


## Runtime backup and restore

While Mitos is serving, create a new private backup directory on the server:

```sh
./bin/mitos storage backup --out /path/to/new-backup
```

The SQLite online backup includes committed WAL data, canonical events, exact saved
agent-library revisions, registered artifact bytes, the MQTT signing key and stored API-key files. It
holds admission while copying; readers remain available. Environment credentials,
original projects and native CLI conversation storage remain separate. An active
process is not preserved by a database backup.

Restore into a new data directory, then start the server with that directory:

```sh
./bin/mitos storage restore --from /path/to/new-backup --data /path/to/restored-data
./bin/mitos serve --data /path/to/restored-data
```

Restore verifies the event chain and artifacts before publishing the new directory,
and advances the authority epoch to fence pre-restore grants and tokens. It refuses
an existing destination. Receipt expiry through `storage expire-receipts --before
2026-11-01T00:00:00Z` retains rejection tombstones, so an expired key cannot start
work again. Run and artifact history remains retained until data-directory removal.


## Application access

Keep operator access on loopback and enable the application API separately:

```sh
./bin/mitos serve --application-addr :4747 --application-cert server.pem --application-key server-key.pem
./bin/mitos principal create --id my-app --name 'My application' --org example --project assistant --write
```

The operator command returns a random bearer token once. Only its hash is saved.
Send that token over TLS with `Authorization: Bearer TOKEN` and
`Mitos-API-Version: 1`. The listener accepts headless structured work and exposes
only the authenticated tenant's tasks, runs, events, results and artifacts. Foreign
IDs return 404; workspace, graph, project and operator routes are absent. The
local console remains the operator. Tenant-scoped request keys bind the original
principal and payload; another principal cannot take over the same key.

Use `principal list`, `principal rotate --id my-app` and `principal revoke --id
my-app` on the local operator API. Rotation, revocation, expiry and restore fencing
apply to existing connections and cached views. Principals are read-only unless
`--write` is supplied and default to `mitos.generalist`; repeat `--agent AGENT_ID`
to permit other worker definitions. Bounded feeds, configurable capacity, tenant limits and the separate application
crash fixture demonstrate S2 application-boundary conformance. PostgreSQL remains
deferred. See the [application API](docs/application-api.md) for pagination,
operator runtime policy, confined application execution prerequisites and the fixture.


The independently versioned [worker protocol](docs/worker-protocol.md) supports
replaceable headless workers, pool-only MQTT wake hints and a durable external
action gateway. The S3 subprocess fault matrix demonstrates worker and authority
recovery, lost receipt replay, missed notifications and original-operation
reconciliation. The [personal/negotiation fixture](examples/conversationfixture/README.md)
demonstrates durable waits, serialized personal updates, current-context publication
and private-thread confinement. The [S4 scale report](docs/benchmarks/s4-dormant-scale.md)
measures 20 tenants, 2,000 dormant responsibilities and 8 active worker identities.
