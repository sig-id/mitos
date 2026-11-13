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
its lightweight defaults. PostgreSQL runtime storage, tenant isolation and remote
worker claims are planned capabilities, not available deployment options today.
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
assignment, formatted report, logs, changes, or attempts. The planner reviews saved
evidence before completing a goal; process exit alone never accepts worker output.
Changes stay in workspace copies, with patches and complete workspace artifacts.
Older untouched goals require **Start automatic work**; viewing them does not launch
anything. See [automatic workflow](docs/adaptive-runtime.md#automatic-workflow).

Use **Start agent session** to launch one Codex or Claude session through Mitos.
The terminal opens in the browser; supported questions and approvals appear in
**Inbox**. Select an installed CLI and environment profile, then enter an assignment.
The CLI equivalent is `./bin/mitos agent codex --workspace /path/to/project` while
Mitos is serving. Add `--keep-open --prompt "Assignment"` to supply the initial
assignment and keep the conversation open. Codex supports live turn steering and asynchronous clarification replies.
Bridge resume/fork is not yet supported; ordinary native sessions remain available below.

Installed interactive agent terminals are separate executors. **Sessions & earlier
work → New native session** retains their assignment, supervision inbox, reports,
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
work, atomic graph amendments, automatic application dispatch, registered controller
applications, and optional typed decisions. Native worker output stays intact.
The project **Work** page creates and displays these graphs. `/api/graphs` provides
the programmatic interface; `/api/graphs/capabilities` reports the implemented
adapter and remaining integrations. Native PTY sessions and services retain their
separate lifecycle.

## Agent library

The [default library](agents/README.md) ships eight adaptable definitions ported from
Chief Wiggum. The native Codex adapter loads these definitions, freezes them for each
attempt, and supports graph-scoped specializations. Custom worker definitions can be
added or edited in **Agent library**; subsequent goals make them available to the
planner. The full [agent-library specification](spec/system/07_AGENT_LIBRARY.md)
additionally describes future runtime negotiation, knowledge and publication features.

## Optional decision models

Command applications can add `decision_model` to choose whether to start a segment
or take an already eligible recovery. Mitos supports the Jev/Laya System One HTTP
format and OpenAI Decisions through separate adapters. Configure the full endpoint,
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
```

Work and interactive metadata are reconstructed from `.mitos/events.jsonl`. On first
startup with this model, the server imports `desk/state.json` and retains that file
as the pre-migration backup; it is no longer updated. Keep backups of the complete
data directory, including terminal output and batch artifacts. Interrupted
work retains output, partial changes, and recovery decisions. A retry gets a new
Run ID under the same Task. Silence does not authorize a pending local decision.

The local provider runs as the operator and can access the host. Batch environment
hygiene and a separate verification directory do not establish confinement. Strict
unsupported compute requirements are refused. Interactive agents use their native
credentials and configuration; free-text project rules are instructions.

The main server binds to loopback and rejects foreign Host headers and cross-origin
mutations. The optional HTTPS device API has restricted snapshot and decision scope;
it is not the full remote-client contract. See
[API and compatibility](spec/system/05_INTERFACES_AND_INTEGRATIONS.md).

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
| `internal/web/` | Server-rendered browser interface and vendored assets |
| `examples/` | Runnable applications |
| `spec/` | Product, six system specifications, and ADRs |

Use `just check` for source changes. Documentation changes should preserve local
links, consecutive numbering, and the distinction between requirements and shipped
capabilities. Update the owning specification and ADR when architecture changes.

## License

Mitos is licensed under the [MIT License](LICENSE). Bundled third-party components
retain their accompanying license notices.
