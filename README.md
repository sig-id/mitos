# Mitos

Mitos is an operating system for agents, under active development. A server owns
work, decisions, and execution; clients connect to direct and inspect it. The goal
is to preserve a responsibility through failed attempts, replacement agents, and
changes of client, with explicit authority and verified results.

Start with the [product specification](spec/product/01_PRODUCT.md) for the purpose,
market, and intended experience. The [specification index](spec/README.md) links to
six system contracts and the architecture decisions. These are normative targets;
[delivery and conformance](spec/system/06_DELIVERY_AND_CONFORMANCE.md) distinguishes
current behavior from remaining work.

Today Mitos provides a local Go server, browser workspace, and CLI. Interactive
agent sessions and durable batch tasks are useful foundations with separate stores
and lifecycles; their unification is still planned.

## Start

Requires Go 1.26+, Git, and Bash on Linux. The first build downloads pinned Go
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

Open a project directory, choose an installed agent, and select **Analyze project**.
Review the proposed context and goal, then **Start supervisor**. Analysis produces
a draft; it does not itself change the saved profile or start working sessions.
The supervisor delegates to real managed agents with recorded relationships.

You can also **Add agent** directly. Interactive agents use native CLI configuration.
Terminals support input, resize, scrollback, interrupt, and stop. The session menu
provides supported resume, fork, and handover operations. Topics support discussion
and discovery; they do not confer execution authority.

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

Batch history is reconstructed from `.mitos/events.jsonl`; interactive metadata has
its own desk state store. Keep backups of the complete data directory. Interrupted
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

## Repository

| Path | Contents |
|---|---|
| `cmd/mitos/` | Server and HTTP-backed CLI |
| `internal/studio/` | Batch state, applications, and execution |
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
