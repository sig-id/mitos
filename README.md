# Mitos Studio

Run coding CLIs in a shared project workspace. Keep their terminals visible,
discover coordination topics, inspect changes, and share managed builds.

Mitos is being rebuilt in **Go + HTMX**, starting with the Studio product in
the specs. The previous `beam/` implementation has been removed. This is a new
implementation, not a feature-complete port of that runtime.

## Start

Requires Go 1.26+, Git, and Bash on Linux (validated with Git 2.54). The first
build downloads the pinned Go PTY dependency. No Node, frontend compilation,
database service, or CDN is required to serve it. Terminal assets are vendored.

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

The Studio opens on the **agent workspace**. **Open a project** → select a local
directory → **New session** → enter your installed CLI command (or leave it empty
for a shell). Sessions share the directory and use your local CLI configuration.
Select **Project coordinator** for the session that manages the plan; only one
active coordinator is allowed per project. The role provides coordination context,
not extra permissions or an automatic approval policy.

The terminal has real PTY input, resize, scrollback, interrupt, and stop controls.
Navigation leaves execution running. Output survives server restart; processes
are not automatically relaunched. Interrupted sessions require explicit process
inspection before replacement. See [interactive workspace behavior and APIs](docs/INTERACTIVE_WORKSPACE.md).

The existing batch workflow is available from **Task history** → **New work** → choose **Workspace inventory** →
choose a local directory → start. It copies the workspace, inventories actual
files, records their content hashes, checks the saved artifact, and delivers
`inventory.json` and `report.md`. This built-in application performs no model
calls or code-quality assessment. The optional **Workspace note** example
executes a real command and an independent acceptance command.

## The product

- **Agent workspace:** projects, actual CLI sessions, a topic board, working-directory
  diffs, observed processes, session scratch, and managed Cargo builds.
- **Topic board:** compact discovery summaries, bounded thread reads, revision-checked
  replies, and durable follow cursors. Agents use the same API through `mitos desk`.
- **Execution overview:** decisions needing attention, current work, recent outcomes, and
  installed applications.
- **Work:** searchable history; each attempt has a brief, execution record,
  output, acceptance evidence, and a standalone report.
- **Decisions:** explicit choices with context and consequences. Interrupted
  execution parks until the operator chooses to restart or close it.
- **Applications:** installed entrypoints; author in your editor and launch
  from Studio.

The workspace follows the [revised product design](docs/ORCHESTRATION_REDESIGN.md),
with current boundaries in [interactive workspace behavior](docs/INTERACTIVE_WORKSPACE.md).
Batch execution retains `15_DECISION_PLANE` and `16_AUTONOMY`'s decision and
acceptance boundaries. See [the product/design decisions](docs/PRODUCT_DESIGN.md) and
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
  "acceptance": {
    "cmd": ["/absolute/path/to/verifier"],
    "oracle_paths": ["tests/acceptance"],
    "timeout_ms": 60000
  }
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

The application runs in a copied workspace with its own Git repository. Its
stdout/stderr is saved as `output.txt`. After the command stops, Studio saves
`baseline.json`, `changes.patch`, `changes.json`, and `workspace.tar.gz`, including
partial changes after command failure, cancellation, or timeout. Result capture
has the same 64 MiB / 10,000-file ceiling; a harvest failure records its cause
and preserves the raw workspace and command output.

Acceptance runs from a snapshot of the installed application directory made
before execution. It receives the same request shape with `workspace` (and
`MITOS_WORKSPACE`) pointing to a separate result reconstructed from the pristine
baseline and harvested patch. Executables inside the application directory are
rebased onto the saved copy. Relative script references resolve there; absolute
paths embedded inside command arguments remain the adapter author's responsibility.
Changes intersecting `oracle_paths` park the run before acceptance. Exit 0
records a local pass; another exit fails the run with evidence. The optional
`measure` command emits a JSON number or numeric vector; its baseline, result,
and delta are recorded even when the acceptance predicate fails.

The acceptance timeout defaults to 60 seconds and bounds reconstruction,
verification, and measurements within the overall execution limit. Existing
argv-array `acceptance` declarations still load. The example predicate checks
the note's exact content. See [workspace and acceptance contracts](docs/WORKSPACES.md).

Processes receive `PATH`, isolated `HOME`/`TMPDIR`, `LANG`, `MITOS_WORKSPACE`,
and Git settings that neutralize host configuration and ancestor discovery.
Provider credentials are not inherited. Broker integration remains outstanding.
Combined stdout/stderr is capped at 2 MiB per command.

**This is a trusted local process provider, not a sandbox.** Processes run as
the operator and can access the host. The external verifier's working directory
separates execution from the copied workspace, but does not enforce an
inaccessible oracle. Untrusted applications, T2 isolation, credential brokering,
and the full autonomy gate require the integrations listed in the implementation
status. The server binds only to loopback and rejects foreign Host headers and
cross-origin mutations. There is no hosted identity or public buyer surface yet.

Command execution and harvest now use a provider interface. Applications can
declare strict `compute` requirements; unsupported tiers, broker enforcement,
or providers are refused before a run is created. Existing apps without `compute`
retain an explicitly recorded local development posture. See [provider admission
and lifecycle](docs/COMPUTE.md); no strict sandbox provider is registered yet.

## Observation and interruption evidence

Installed commands can opt into activity observation and a normalized JSONL
session tail. A quiet command produces a recorded stall and an operator decision;
its output and workspace are preserved. See [the observation contract and
configuration](docs/OBSERVATION.md). This first adapter samples local processes
and does not grant unattended operation.

```sh
./bin/mitos interrupts                      # recorded requests and human touches
./bin/mitos interrupts --logs .runs          # read-only historical corpus audit
```

The report excludes labelled demo runs, verifies current log chains when reading
files, and states missing evidence instead of estimating a distribution from no
human resolutions.

The **Autonomy evidence** link on Overview reports verified progress, decision
precision, checkpoint green rate, and rewind depth. Missing measurements remain
unknown; censored runs contribute horizon bounds rather than infinite durations.

```sh
./bin/mitos autonomy
./bin/mitos autonomy --logs .mitos   # read-only, verified log replay
```

New command plans record their unattended grant separately from measured time.
The current local provider receives zero. See [metric definitions and corpus
coverage](docs/AUTONOMY_METRICS.md).

## Human decisions

Cards bind structured context and artifact references to a run, a causal request,
and its local tenant. The console, CLI, and API record the same resolution with
server-supplied resolver identity and time. Duplicate choices are audited; the
first valid choice wins. Accepted retries resume after restart without creating
another child attempt.

```sh
./bin/mitos decisions
./bin/mitos resolve --choice cancel --note 'Keep the evidence.' local_RUN_ID decision_ID
```

Local command cards remain external: silence never authorizes them. See
[decision policy, replay, and the local identity boundary](docs/DECISIONS.md).
Hosted identity, automatic defaults, and notification/digest transport remain
outstanding.

## Checkpoints and recovery

Installed command runs record checkpoint commits and predicate evidence after
stopped segments. Optional `supervision` configuration adds bounded recovery from
a passing checkpoint, retained failure memos, and predicate-stall escalation.
Run pages show saved points and offer recovery on eligible parked decisions.

```sh
./bin/mitos checkpoints local_RUN_ID
./bin/mitos rewind --from-segment 1 --memo 'Repair the recorded failure.' local_RUN_ID
```

Recovery preserves earlier workspaces and artifacts, uses a new segment, and keeps
the original execution deadline. Later artifact names start with `segment-002-`
and so on. Configured supervision probes the initial predicate before work starts;
ordinary apps retain an unknown initial checkpoint. See [configuration, evidence,
and crash behavior](docs/RECOVERY.md). This is local single-step recovery; composed
pipeline recovery and enforced oracle isolation remain outstanding.

## Data and execution

`--workspace-root` bounds which directories can be selected. Intake uses Git's
ignore rules: tracked files plus untracked, non-ignored files, including nested
`.gitignore` rules. Plain directories use a temporary external Git index; their
source is not initialized as a repository. Intake omits symlinks, Git internals,
and this Studio's data directory. Unsupported special files and submodule
materialization are refused. The 64 MiB / 10,000-file preflight names the largest
contributors when it refuses a workspace. Source branches and indexes remain
untouched.

Two runs execute concurrently, oldest queued first. Stop requests enter
**Stopping**, kill the process group, and allow up to 60 seconds for harvest
before closing the run. Shutdown also stops commands and preserves available
changes. Interrupted runs park on restart instead of replaying possible external
effects; recorded pending checkpoint restorations can resume, and a previously
recorded stop request stays stopped. A retry gets a new
run ID and retains the same task ID. Repeated retry submissions return the same
child attempt; retry the latest attempt to run again.

`.mitos/events.jsonl` is the canonical append-only record, fsynced before
execution. State is reconstructed from events; a process lock excludes a second
writer. A partial final record is truncated on startup; a damaged complete
record stops startup. Hash chaining detects record modification, but is not an
external authenticity anchor. An append/fsync error poisons the writer until
restart. Artifact downloads verify their recorded SHA-256 digest. Keep backups
of the complete data directory; workspace retention/GC is manual.

Disk writes release the state read lock, so Work pages and status APIs can read
the last durable state while a flush is pending. Execution and verification
decisions share a flush with their adjacent progress markers. Artifacts from one
harvest share directory and log flushes; already-persisted parent directories
are not flushed again. Submissions and execution barriers still wait for durable
storage, but slow flushes do not hold up status reads.

The old ignored `.runs/` records are left in place and are not automatically
imported. The new log format is different.

## CLI and HTTP

All clients use the same Go operations. The CLI connects to a running Studio:

```sh
./bin/mitos run --title 'Inspect this workspace' --workspace . --minutes 5
./bin/mitos status local_RUN_ID
./bin/mitos log local_RUN_ID
./bin/mitos fleet
./bin/mitos evidence local_RUN_ID
./bin/mitos tasks
./bin/mitos task task_TASK_ID
./bin/mitos pools
./bin/mitos services
./bin/mitos services-preview --file services.json
./bin/mitos services-configure --file services.json --expected-revision 0
./bin/mitos service-run --key inspection-1 inventory
./bin/mitos configure-pool --discipline fifo --concurrency 1 batch
```

Use `--url http://127.0.0.1:4756` before the run ID for another port.

Command applications default to owning the whole source workspace. Use repeated
`run --own-path src/component --own-path tests/component --lease database:integration`
flags to declare a narrower scope and singleton resources, or the equivalent new-work
form fields / JSON brief `ownership` object. Conflicts wait before dispatch;
out-of-scope changes are preserved but cannot pass acceptance. Crash-held claims
require inspection and explicit `release-claims --confirmed-stopped RUN_ID`.
See [local fleet coordination and evidence](docs/FLEET.md) for lifecycle, replay,
reader bounds, and the outstanding supervisor isolation boundary.

| Method | Path | Result |
|---|---|---|
| GET | `/api/interrupts` | Recorded interruption distribution and coverage limits |
| GET | `/api/tasks` | Durable Task state, scheduling, and admitted attempt lineage |
| POST | `/api/tasks` | Submit a JSON brief and return its Task |
| GET | `/api/tasks/{id}` | One Task and its prerequisite bindings |
| GET | `/api/services` | Desired project activation and reasons |
| POST | `/api/services/preview` | Validate a catalog without saving |
| PUT | `/api/services` | Save `catalog` with `expected_revision` |
| POST | `/api/services/{id}/lifecycle` | Stop, restart, or resume a unit/target group with an idempotency key |
| GET | `/api/service-lifecycle` | Latest 50 service stop operations |
| GET | `/api/service-lifecycle/{seq}` | Stop scope, state, and bounded Task witnesses |
| POST | `/api/services/{id}/trigger` | Submit a manual service Task with `idempotency_key` |
| GET | `/api/service-events` | Latest 50 custom event summaries |
| GET | `/api/service-events/{seq}` | One custom event, payload, and recipient progress |
| POST | `/api/service-events` | Emit `name`, `idempotency_key`, and JSON `payload` |
| POST | `/api/services/{id}/signals` | Deliver a named source, delivery ID, and JSON payload |
| POST | Configured webhook path | Deliver JSON with `Idempotency-Key` and optional `X-Mitos-Cycle` |
| GET | `/api/pools` | Registered pool policies |
| PUT | `/api/pools/{id}` | Persist a matching JSON pool configuration |
| GET | `/api/fleet` | Bounded structured run summaries and claim state |
| GET | `/api/runs/{id}/evidence` | Bounded structured evidence and its log anchor/digest |
| POST | `/api/runs/{id}/claims/release` | Release inspected claims with JSON `confirmed_stopped: true` |
| GET | `/api/autonomy` | Autonomy metrics, bounds, and recorded grants |
| GET | `/api/decisions` | Unanswered human decision cards |
| GET | `/api/runs/{id}/decision` | Current card and recorded resolution |
| POST | `/api/runs/{id}/decisions/{decision}` | Resolve JSON `choice` and optional `note` |
| GET | `/api/runs` | Run history |
| POST | `/api/runs` | Validate and submit a JSON brief |
| GET | `/api/runs/{id}` | Folded run |
| GET | `/api/runs/{id}/events` | Recorded events |
| GET | `/api/runs/{id}/checkpoints` | Saved recovery points and predicate evidence |
| POST | `/api/runs/{id}/rewind` | Validate and queue a checkpoint restoration |
| POST | `/api/runs/{id}/heartbeat` | Record a current service monitor's increasing heartbeat sequence |
| POST | `/runs/{id}/cancel` | Stop work |
| POST | `/runs/{id}/retry` | New attempt, same task |
| POST | `/runs/{id}/decisions/{decision}` | Resolve form field `choice` |
| GET | `/runs/{id}/report` | Standalone operator report |
| GET | `/runs/{id}/artifacts/{name}` | Verified download |

Task readiness is separate from run execution. Use `run --pool batch --after task_ID`
for a successful prerequisite, or `--after-failure task_ID` for a failure branch.
`--join any` and `--join k_of_n --quorum K` select joins; `--deadline RFC3339`
bounds queue time and execution. The Work page links to task state and pool policies.
Use repeated `--input TASK_ID:ARTIFACT_NAME` flags to consume explicitly selected
upstream artifacts as verified private copies. The new-work form supports the same
selection. See [bound data dependencies](docs/DATA_DEPENDENCIES.md) for limits,
worker input paths, integrity checks, and recovery.
See [durable tasks and work pools](docs/WORK_POOLS.md) for supported edges,
selection, replay, and outstanding work-pool mechanisms.

## Structure

```text
cmd/mitos/             server + HTTP-backed CLI
internal/studio/      events, durable store, local applications, execution
internal/observation/ quiescence clock, local activity, normalized session tails
internal/workspace/   Git-aware snapshots, private history, patch and archive harvest
internal/acceptance/  predicate manifests, oracle path checks, numeric measurement
internal/autonomy/    checkpoint records, restart intensity, predicate-stall policy
internal/decision/    canonical human cards, resolution data, reversibility policy
internal/compute/     provider lifecycle, local process execution, launch admission
internal/fleet/       ownership conflicts and structured evidence schemas
internal/desk/        local PTY sessions, topic board, managed builds, resource views
internal/workpool/    dependency readiness, deterministic disciplines, pool capacity
internal/web/         HTTP routes, embedded Go templates, CSS, HTMX
examples/             runnable local application contract
docs/                 product and UX rationale
spec/                 target contracts and current implementation status
```

Batch HTML forms and links work without JavaScript. HTMX enhances navigation, search,
and live run updates using server-rendered HTML ([HTMX documentation](https://htmx.org/docs/)).
The interactive workspace uses vendored xterm.js 5.5.0 and fit addon 0.10.0,
with their MIT licenses under `internal/web/static/`; it requires JavaScript.
HTMX 2.0.10 is vendored with its BSD 2-Clause license under
`internal/web/static/`. The remaining UI uses system fonts and local assets.

The Work page also links to **Services**: preview/save project targets, presets,
requirements, optional members, conflicts, and activation reasons. Active manual,
interval, cron, event, and continuous
services create Tasks through the existing pools, with durable schedule cursors
and bounded per-service concurrency/queueing. See [service composition and
triggers](docs/SERVICES.md) for a catalog example and idempotency/replay behavior.
Provider-specific ingress connectors and the remaining service runtime are still outstanding.

Optional [service circuit breakers](docs/SERVICE_BREAKERS.md) persist failure counts,
cooldowns, and bounded recovery probes. They gate queued execution before claims
and retain state across restart.

[Service restart policies](docs/SERVICE_RESTARTS.md) add bounded retries with saved
backoff/deadlines and per-service abort. Pending retries preserve Task dependency
semantics and can be cancelled from the Services screen.

[Service cron schedules](docs/SERVICE_CRON.md) add timezone-aware calendar execution,
saved clock previews, and distinct occurrences across daylight-saving folds.

[Service outcome chaining](docs/SERVICE_EVENTS.md) creates downstream Tasks from
final success/failure outcomes, with durable delivery, source evidence, capacity
reservations, and shared depth/fanout limits across feedback branches.

[Custom service events](docs/SERVICE_CUSTOM_EVENTS.md) deliver named JSON payloads
to active subscribers through `mitos emit-event`, the API, or the Services page.
Durable source receipts preserve idempotency, payload evidence, subscriber cycles,
and delivery limits across restart; `mitos service-events [SEQ]` reads them.

[Service lifecycle](docs/SERVICE_LIFECYCLE.md) adds durable admission holds
and shutdown across `part_of` members. Use `mitos service-stop`,
`mitos service-restart`, `mitos service-resume`, and `mitos service-lifecycle`, or the Services page.
Resume waits for worker exit, claim release, and pending delivery suppression.

[Continuous services](docs/SERVICE_CONTINUOUS.md) maintain one managed Task, wait
through its retries, and save a restart delay on completion. Disabling the service
stops its managed work and preserves available execution evidence.

[Service queue priority](docs/SERVICE_PRIORITY.md) adds low/normal/high/critical
preferences to priority-pool scoring. Task bindings survive catalog edits, event
delivery, retries, and replay.

[Service replacement](docs/SERVICE_REPLACEMENT.md) supports `if_running: "kill"`.
It saves the exact prior Task, stops it through the normal harvest path, and gates
the new execution until shutdown and claim release are complete.

[Service health checks](docs/SERVICE_HEALTH.md) monitor command workers through
file freshness, bounded shell probes, or scoped heartbeats. Unhealthy actions can
log, stop, or use bounded Task retries while retaining output and workspace changes.
Workers can send heartbeats with `mitos heartbeat --monitor ID --sequence N RUN_ID`.

[Service conditions](docs/SERVICE_CONDITIONS.md) gate execution on file globs,
environment comparisons, and bounded command checks. Mismatches keep the trigger
receipt and skip application execution without acceptance or failure retries.

[Service completion dependencies](docs/SERVICE_DEPENDENCIES.md) bind `depends_on`
to successes in the same scheduler cycle. Manual triggers share `--cycle KEY`;
admission saves exact prerequisite Task/Run/event witnesses and keeps them separate
from ordinary Task joins.

[Webhook and external-signal services](docs/SERVICE_INGRESS.md) accept bounded JSON
deliveries through configured webhook paths or `mitos service-signal`. Delivery
IDs, payload digests, and fixed work bindings are saved atomically with Task
receipts; identical redeliveries return the original receipt after restart.
