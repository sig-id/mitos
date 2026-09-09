# Mitos Studio

Give work a brief. Make decisions by exception. Return to a result with evidence.

Mitos is being rebuilt in **Go + HTMX**, starting with the Studio product in
the specs. The previous `beam/` implementation has been removed. This is a new
implementation, not a feature-complete port of that runtime.

## Start

Requires Go 1.26+ on Linux. No Elixir, Node, frontend compilation, database
service, or network dependency is needed to build or serve it.

```sh
just serve                 # http://127.0.0.1:4646
just demo 4756             # labelled example history, separate .mitos-demo data
just check                # vet + race-enabled tests
```

Without `just`:

```sh
go run ./cmd/mitos serve --apps ./examples
go test -race ./...
go build -o bin/mitos ./cmd/mitos
```

The real studio starts empty. **New work** → choose **Workspace inventory** →
choose a local directory → start. It copies the workspace, inventories actual
files, records their content hashes, checks the saved artifact, and delivers
`inventory.json` and `report.md`. This built-in application performs no model
calls or code-quality assessment. The optional **Workspace note** example
executes a real command and an independent acceptance command.

## The product

- **Overview:** decisions needing attention, current work, recent outcomes, and
  installed applications.
- **Work:** searchable history; each attempt has a brief, execution record,
  output, acceptance evidence, and a standalone report.
- **Decisions:** explicit choices with context and consequences. Interrupted
  execution parks until the operator chooses to restart or close it.
- **Applications:** installed entrypoints; author in your editor and launch
  from Studio.

The design follows `02_PRODUCT`, `11_FRONTEND`, `15_DECISION_PLANE`, and
`16_AUTONOMY`. See [the product/design decisions](docs/PRODUCT_DESIGN.md) and
[the implementation boundary](spec/00_IMPLEMENTATION_STATUS.md).

## Local applications

Start with [examples/local-app/app.json](examples/local-app/app.json). Each
directory supplied through `--apps` contains an `app.json` with:

```json
{
  "id": "my-application",
  "name": "My application",
  "description": "The outcome this application produces.",
  "entrypoint": "Run the application",
  "command": ["/absolute/path/to/runner"],
  "acceptance": ["/absolute/path/to/verifier"]
}
```

The runner receives JSON on stdin:

```json
{
  "run_id": "local_...",
  "brief": {
    "title": "A useful result",
    "intent": "Desired outcome and constraints",
    "workspace": "/original/workspace",
    "app_id": "my-application",
    "timeout_minutes": 10
  },
  "workspace": "/data/workspaces/local_.../workspace"
}
```

The application runs in the copied workspace. Its stdout/stderr is saved as
`output.txt`. The verifier receives the same JSON and runs **from the installed
application directory**, outside the workspace, with `MITOS_WORKSPACE` pointing
to the result. Exit 0 records acceptance; any other exit fails the run and
preserves the output. Verification has a 60-second cap within the overall limit.
The example verifier checks only that a file exists; replace it with a meaningful
acceptance predicate for real work.

Only `PATH`, isolated `HOME`/`TMPDIR`, `LANG`, and `MITOS_WORKSPACE` enter these
processes. Provider credentials are not inherited. Runner configuration and
any needed credentials must be implemented by the trusted adapter; the broker
integration is not implemented yet. Combined stdout/stderr is capped at 2 MiB
per command. Other generated files remain in the run's saved workspace; this
build does not yet harvest arbitrary generated files or patches.

**This is a trusted local process provider, not a sandbox.** Processes run as
the operator and can access the host. The external verifier's working directory
separates execution from the copied workspace, but does not enforce an
inaccessible oracle. Untrusted applications, T2 isolation, credential brokering,
and the full autonomy gate require the integrations listed in the implementation
status. The server binds only to loopback and rejects foreign Host headers and
cross-origin mutations. There is no hosted identity or public buyer surface yet.

## Data and execution

`--workspace-root` bounds which directories can be selected. Copies omit
symlinks, VCS internals, common generated directories, and this Studio's data
directory. They are limited to 64 MiB / 10,000 files. This is an explicit
exclusion list, **not** full `.gitignore` support. Source workspaces are never
used as command working directories.

Two runs execute concurrently, oldest queued first. Each run has a time limit;
stop and shutdown kill its local process group. Interrupted runs park on
restart instead of replaying possible external effects. A retry gets a new run
ID and retains the same task ID. Repeated retry submissions return the same
child attempt; retry the latest attempt to run again.

`.mitos/events.jsonl` is the canonical append-only record, fsynced before
execution. State is reconstructed from events; a process lock excludes a second
writer. A partial final record is truncated on startup; a damaged complete
record stops startup. Hash chaining detects record modification, but is not an
external authenticity anchor. An append/fsync error poisons the writer until
restart. Artifact downloads verify their recorded SHA-256 digest. Keep backups
of the complete data directory; workspace retention/GC is manual.

The old ignored `.runs/` records are left in place and are not automatically
imported. The new log format is different.

## CLI and HTTP

All clients use the same Go operations. The CLI connects to a running Studio:

```sh
./bin/mitos run --title 'Inspect this workspace' --workspace . --minutes 5
./bin/mitos status local_RUN_ID
./bin/mitos log local_RUN_ID
```

Use `--url http://127.0.0.1:4756` before the run ID for another port.

| Method | Path | Result |
|---|---|---|
| GET | `/api/runs` | Run history |
| POST | `/api/runs` | Validate and submit a JSON brief |
| GET | `/api/runs/{id}` | Folded run |
| GET | `/api/runs/{id}/events` | Recorded events |
| POST | `/runs/{id}/cancel` | Stop work |
| POST | `/runs/{id}/retry` | New attempt, same task |
| POST | `/runs/{id}/decisions/{decision}` | Resolve form field `choice` |
| GET | `/runs/{id}/report` | Standalone operator report |
| GET | `/runs/{id}/artifacts/{name}` | Verified download |

## Structure

```text
cmd/mitos/             server + HTTP-backed CLI
internal/studio/      events, durable store, local applications, execution
internal/web/         HTTP routes, embedded Go templates, CSS, HTMX
examples/             runnable local application contract
docs/                 product and UX rationale
spec/                 target contracts and current implementation status
```

HTML forms and links work without JavaScript. HTMX enhances navigation, search,
and live run updates using server-rendered HTML ([HTMX documentation](https://htmx.org/docs/)).
HTMX 2.0.10 is vendored with its BSD 2-Clause license under
`internal/web/static/`. The remaining UI uses system fonts and local assets.
