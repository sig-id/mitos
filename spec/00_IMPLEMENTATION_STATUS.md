# Current implementation and technology decision

**2026-09-16: interactive workspace increment.** The default UI now hosts real
local PTY sessions in shared project directories, with a visible coordinator
role, progressive topic discovery, managed Cargo jobs, Git/process views, and
session scratch retention. This is a separate trusted-local execution path; it
does not upgrade batch provider isolation, verification, or unattended grants.
See [implemented behavior and limits](../docs/INTERACTIVE_WORKSPACE.md). The earlier
batch screens remain available at `/overview`, `/work`, and their existing routes.

**2026-09-08: replace the BEAM implementation with Go + HTMX and redesign the
product from these specs.** This decision supersedes implementation-language
requirements and old frontend navigation in the remaining specs. Product
contracts and invariants remain targets, not claims of implemented features.

The old `beam/` tree, build artifacts, Mix configuration, tests, and runtime
applications are removed. Repository-root Go tooling is the only implementation.
Historical Elixir callback examples in detailed specs describe target semantics;
future Go interfaces must preserve those semantics rather than their syntax.
External engine, Nucleus, and sigid boundaries remain external.

## Implemented

- Go server/CLI and embedded HTMX UI; no frontend build or CDN dependency.
- New Studio navigation: Overview, Work, Decisions, Applications.
- Loopback Studio operation with cross-origin mutation protection. An optional
  separate TLS device listener exposes scoped snapshots, durable decision
  receipts, and voice drafts; local CLI pairing, rotation, expiry, revocation,
  tenant isolation, bounded pagination, and restart replay are covered by backend
  tests. Codex app-server and Claude hook adapters ingest native questions and
  one-shot approvals, validate text answers, journal native responses, and record
  source acknowledgement/session invalidation. Real installed harness round-trip
  tests pass with local deterministic inference. HTTP and executable STT providers
  produce drafts only. See [device API setup and validation](../docs/DEVICE_API.md).
  Physical esp-notify board acceptance remains external.
- Durable task/run identity, JSONL Decision/Effect records, hash chaining,
  fsync-before-execute, locked writer, replay, and partial-tail recovery.
- Brief validation, bounded workspace copying, concurrent local execution,
  fixed installed commands, cancellation, timeouts, retries.
- Provider lifecycle interface and local process implementation used by commands,
  observation, stop, and workspace harvest. Persisted preparation binds identity
  and posture; source preparation binds its own protected Git baseline.
- Deterministic launch admission, explicit local development descriptors, and
  refusal of unsupported strict/T2 requests before run creation. No broker or
  confinement capability is attributed to the development process provider.
- Built-in workspace inventory with actual hashes and a portable report.
- Small JSON local-app dialect: one entrypoint, command plus acceptance.
- Git-aware bounded intake with nested ignore rules, named size offenders,
  unchanged source index/branch, private worker Git metadata and protected baseline.
- Binary-capable patch, changed-path manifest, and complete result archive harvest
  after success, failure, timeout, or cancellation; failed harvest preserves the
  raw workspace and records its error.
- Canonical acceptance objects with legacy array reading, saved installed verifier
  files, pristine-baseline + patch reconstruction, deterministic oracle-path checks,
  judgment escalation, numeric baseline/result/delta evidence, and replayed verdicts.
- Separately executed acceptance; saved output and checksum-checked downloads.
  **The local same-user provider does not enforce oracle isolation.**
- Interrupted-run recovery and explicit, idempotent external decision cards.
- Structured human cards bound to their request Decision, run, local tenant,
  folded context, and evidence references; deterministic reversibility classifier
  and default/TTL schema constraints. The same-user provider still issues only
  external cards and never defaults them.
- Shared console/API/CLI resolution with server-supplied local principal and time,
  tenant checks, immutable first resolution, audited rejection/duplicates, pending
  card recovery, and restart-safe application of accepted retry/rewind choices.
- Commit-bound local checkpoints with recorded quiescence/predicate evidence,
  durable rewind Decisions and restore Effects, independent restored Git trees,
  failure memos, retained superseded segments, and original-deadline recovery.
- Optional single-step restart supervision with run/step intensity across signals,
  predicate-stall escalation, pending-restore crash recovery, and shared CLI/API/UI
  recovery operations. These mechanisms grant no unattended horizon.
- Opt-in local observation: deterministic quiescence edges, Linux workspace
  mutation watches, sampled process argv digests, durable stall escalation,
  process stop/output preservation, and replayable parked decisions. Procfs
  sampling is incomplete; this adapter grants no unattended horizon.
- Declared normalized JSONL session tails with byte offsets and hashes, bounded
  records, append-only checks, and failure-before-acceptance. Native upstream
  harness schemas are not integrated yet.
- Interruption evidence fold through CLI/API and read-only historical audit;
  current chains are verified and insufficient calibration coverage is explicit.
- Autonomy metrics through the same CLI/API/UI and verified offline reader:
  productive interval unions, censored horizon bounds, unanswered/default precision,
  discarded work per accepted change, and evidence-bound checkpoint green rate.
- Persisted unattended-grant assessment with required observation/predicate/isolation
  facts and the original deadline. Current local execution records a zero grant;
  old records are not retroactively granted one.
- Durable dispatch ownership and singleton-resource claims, conflict waiting,
  harvested-path scope checks, and explicit inspection of crash-held claims.
  Disjoint scopes can run concurrently; saved recovery preserves its claims.
- Bounded structured fleet evidence through shared CLI/API readers, excluding
  narration and raw payloads, with replay-stable log anchors and content digests.
  This reader does not admit an isolated supervisor capability.
- Durable Tasks with separate queue state, retry lineage, task-bound FS control
  dependencies, guarded all/any/quorum joins, frozen prerequisite witnesses, and
  absolute deadlines. Pool-based dispatch now owns all local admission.
- Persisted FIFO/LIFO/priority-with-aging/EDF policies, pool concurrency and
  reservations, replayed pool turns, and shared Task/pool CLI/API/UI controls.
- Explicit FS artifact-data edges with admission-bound result/hash/size witnesses,
  bounded private copies, optional-input omissions, integrity checks before and
  after verification, and fresh copies from frozen bindings on recovery.
- Shared CLI/form artifact selection and replayed input manifests; same-user file
  permissions and inspection do not enforce inaccessible read-only mounts.
- Project service catalogs with deterministic target/preset/override resolution,
  requires bundles, optional members, conflict explanations, and active ordering.
- Revision-checked catalog preview/save and restart-safe idempotent manual triggers
  creating ordinary Tasks, with atomic service provenance and shared CLI/API/UI.
- Durable interval occurrences with jitter, startup behavior, downtime coalescing,
  atomic Task/cursor persistence, capacity skips, and admission rejection evidence.
- Service concurrency and bounded skip/queue backpressure checked before claims,
  with replayed runtime state and visible service wait reasons on Tasks.
- Per-service circuit breakers with atomic failure/outcome tracking, persisted
  cooldowns, bounded recovery probes, generation-bound late-outcome handling,
  pre-claim queue gating, and restart-safe parked-probe reservations.
- Bound service retry/skip/abort policies, capped backoff, preserved retry deadlines,
  nonterminal Tasks during backoff, atomic retry receipt creation, cancellation,
  retry-aware failure guards, and replayed per-service abort state.
- Five-field cron schedules with IANA zones, retained field conjunction/steps,
  Gregorian calendar validation, DST gap/fold handling, persisted initial clocks,
  calendar-aligned catchup, and evidence-backed pause for unavailable future matches.
- Service outcome subscriptions and hooks with atomic terminal delivery allocation,
  exact source Effect provenance, capacity reservations, replay-safe child receipts,
  final retry-aware Task outcomes, and shared causal depth/fanout bounds.
- Continuous service controllers with atomic generation ownership and completion
  delay, retry-aware lifetime, pre-claim retirement fencing, stop/harvest on disable,
  paused intake evidence, and restart-safe recovery of the current managed Task.
- Bound low/normal/high/critical service queue preferences, atomic intake/allocation
  provenance, priority-pool scoring with aging, and retry/replay-stable ordering.
- Bounded kill-on-overlap replacement with exact prior-Task witnesses, atomic
  receipt/allocation intent, indexed victim fencing, idempotent stop requests,
  harvest/claim/worker-exit gating, and replay-safe pending replacement.
- Command-service health checks with confined file metadata, bounded command
  probes, scoped monotonic heartbeats, durable observations/actions, stale-result
  guards, stop/harvest, bounded Task retries, and interrupted-scope recovery.
- Conditional execution for all registered service triggers and builtin/command
  applications: bounded source globs, private environment match evidence, private
  command-copy checks, atomic admission bindings, durable skips, and crash recovery.
- Cycle-bound service completion dependencies, catalog graph validation, shared
  manual/scheduler/event cycle identities, unique per-service receipts, bounded
  dependency waits, and atomic prerequisite success witnesses before claims.
- Local webhook and external-signal intake with configured paths/sources, bounded
  normalized JSON, atomic payload provenance, content-checked delivery idempotency,
  replayed receipts, shared completion cycles, and CLI/API/UI/worker integration.
- Custom named event emission with atomic subscriber allocations, normalized JSON,
  content-checked source keys, shared dependency cycles and root limits, durable
  delivery replay, and CLI/API/UI/worker payload and receipt surfaces.
- Explicit service/target stop and resume with recursive part-of scope, atomic
  admission holds, replay-derived Task witnesses, pending delivery suppression,
  normal cancellation/harvest, worker/claim settlement, and shared CLI/API/UI.
- Explicit service group restart with a durable shutdown/publication barrier,
  fresh same-cycle Tasks, continuous controller ownership, idempotent replay,
  cancellation/rollback of partial preparation, and CLI/API/UI/worker provenance.
- Search, run details, execution timeline, standalone operator reports.
- Labelled, opt-in example history using a separate data directory.

## Not yet implemented

This is a functioning local vertical slice. It satisfies **neither** the Studio
overnight gate nor the Market launch gate in `09`.

| Target | Missing mechanisms |
|---|---|
| Agentic control (`04`) | Model policy, composition, hooks, harness adapters, loop manager |
| Observation (`14`) | Native harness adapters, complete mediated activity, observation admission matrix, persistent quarantine and nightly upstream contracts, terminal snapshots, verified steer |
| Autonomy (`16`) | Enforced inaccessible oracle, dependency-aware multi-step checkpoints/rewind, agent and fleet supervision, admitted positive unattended execution, representative policy/classifier/model replay evaluations |
| Decisions (`15`) | Provider-backed policy grants, automatic default execution, generic value/brief inputs, hold/park rehydration, delivery receipts, cross-worker dedupe, digests, notifications/remote resolution |
| Compute (`05`) | Registered strict provider/engine clients, T2 isolation, full Studio intake delegation, image/data/context mounts, forced egress broker/vault, resource enforcement/metering, explicit in-place mode, provider snapshots, submodule materialization |
| Packaging (`03`, `12`) | Original YAML manifests, typed schemas, content-addressed identity, OCI publish/import |
| Work pools / I/O (`06`, `10`) | Deferred run allocation/generalized payloads, SS/FF/SF edges, typed/generalized data transport, arbitrary guards, batch graph edits, weighted/custom/agent selectors, preemption, invoke, sibling integration tasks, distributed leases/fencing, remaining service triggers/runtime, pipeline service roots, connectors, sinks |
| Memory (`07`) | Retrieval, verification/distillation, supersession |
| Market (`08`) | Identity/tenancy, keys, paid admission, metering, settlement, commercial proof, attribution |

`docs/PRODUCT_DESIGN.md` records the product interpretation. `README.md` records
the actual command, manifest, data, and execution contracts. Sample outcomes
are illustrative; a local acceptance exit code is not the inaccessible oracle
required by `16 §3.2`.

## Latest implementation evidence — 2026-09-09

`docs/OBSERVATION.md` documents the shipped observation adapter and its limits.
`docs/interrupt-baseline.json` records the available historical corpus audit:
11 runs / 133 events, with no supported human resolutions. It does not establish
real two-week coverage or measured think times. Build-order item 0 therefore has
an implemented instrument but an outstanding real-session corpus requirement;
items 1–2 have local mechanisms but still need the external harness/engine seams.
The full Studio and Market gates remain unmet.

Validation for this increment: `go vet ./...`, `go test -race ./...`, and
`git diff --check` pass. A live server smoke test exercised the real 45-second
blocking threshold through HTTP submission, durable stall evidence, saved output,
a rendered decision card, HTTP resolution, and identical API/CLI/offline
interruption reports. Unit and integration checks also cover session replacement
(including inode reuse), partial rows and durable retry offsets, append-only
violations, unknown schemas, workspace renames/moves, contract-failure parking,
replay, and rejection of harness self-reports as acceptance verdicts.


## Workspace and acceptance increment — 2026-09-09

`docs/WORKSPACES.md` specifies the new execution contract. The change record is
pulled after stop and acceptance is checked on its independent reconstruction.
Worker Git metadata cannot change which baseline ordinary harvest uses; source
ignore rules cannot be changed mid-run to hide harvested paths. Cancellation is
now `stopping` until command shutdown and bounded harvest finish.

The inaccessible-oracle requirement is still unfulfilled by a same-user local
process. Verdicts explicitly record `oracle_isolation: same_user_process`; no
unattended horizon is granted. Git commits establish the recovery substrate used
by the subsequent local checkpoint/rewind/supervision increment.

Validation: `go test -race ./...`, `go vet ./...`, and `git diff --check` pass.
The workspace suite verifies nested ignore/negation rules, tracked ignored files,
unchanged dirty source indexes and branches, named size offenders, binary patches,
newlines in paths, deletion/mode changes, worker-history independence, archive
contents, exact patch reconstruction, neutralized host helpers, and rejection of
escaping verification links. Runtime tests cover failed/cancelled/timed-out harvest,
pristine installed verifier files, oracle edit/deletion detection despite ignore
changes, measurement deltas on failed predicates, and replayed commit-bound
acceptance evidence. A live HTTP smoke test ran the example app, downloaded and
applied its patch to a fresh directory, inspected the portable report, and parked
an oracle-changing app while proving the source files remained unchanged.

## Local checkpoint and supervision increment — 2026-09-09

`docs/RECOVERY.md` specifies local checkpoint identities, predicate binding,
restart policy, shared operator actions, and crash behavior. Rewind is recorded
before restoration; fresh segment workspaces and superseded evidence remain
separate. Recovery retains the original execution deadline and carries a failure
memo with the unchanged brief. Saved checkpoint refs retain commits even when
they are absent from the worker clone's default branch refs.

This app dialect still has one `main` step. Dependency-aware tail invalidation,
model-selected orchestration, inaccessible oracle enforcement, provider snapshots,
fleet supervision, and evaluation remain outstanding. The later measurement
increment below supplies autonomy metrics. The full
Studio and Market gates remain unmet.

Validation: `go test -race ./...`, `go vet ./...`, and `git diff --check` pass.
Recovery tests cover restoring exact non-initial commits, fingerprint and Git
metadata mismatch refusal, preservation of abandoned workspaces, failure memo
delivery, explicit non-green selection, restored-input measurement deltas,
restart intensity across signals and clock rollback, predicate plateaus despite
file churn, and repeat-stall escalation. Crash tests cover both sides of restore
publication before the durable Effect, checking that failed commands are not
repeated and deadlines are unchanged. HTTP tests exercise checkpoint display,
shared phone resolution, duplicate submissions, and malformed API requests.

A built-binary live CLI/HTTP smoke test parked a deliberately failed first
segment, listed its checkpoints, restored the initial green point, and completed
segment 2: three checkpoints, one rewind Decision, one restore Effect. It verified
the unchanged source and deadline, downloaded both segments' distinct output,
and downloaded the restored result's full patch.

## Human decision contract increment — 2026-09-09

`docs/DECISIONS.md` describes the bound card, deterministic policy, resolution
provenance, and application recovery. The request itself durably parks execution
before its card Effect. Replaying a pending request reconstructs the saved card;
replaying an accepted retry reuses its one child attempt. Rewind resolutions
validate their memo and target before accepting a human choice. The first valid
resolution remains immutable; later submissions and invalid principals produce
rejection evidence. Local historical string cards still decode without rewriting
the log or manufacturing hosted identity.

New runs stamp the local Studio tenant at creation. This is not sigid integration
or hosted tenant-filtered access. The classifier and nullable default/TTL schema
are implemented, but the same-user provider earns no reversible execution grant.
It issues external cards only. Default execution, remote transport, scheduled
digests, generic value/brief actions, and full canonical control events remain
outstanding; both product gates remain unmet.

Validation: race tests and vet pass. Decision tests cover external-default refusal,
Market default refusal, TTL boundaries, grant-based classification, unknown acts,
exact compensating destinations, invalid principals, stale/conflicting/duplicate
choices, server-bound identity/time, and unsupported fields. Crash tests cover the
request-before-card boundary and both sides of retry child creation. Operator stop
supersedes an unapplied retry. UI/API tests verify structured card rendering and
shared resolution without authority fields supplied by the client.

A built-binary live check listed a real parked card through the CLI, resolved its
rewind, repeated the choice through the resolution endpoint, and restarted the
server. It retained one canonical resolution, one rewind, two segments, the
original resolver/time/transport, and one measured human touch. Source contents,
deadline, and earlier artifacts remained unchanged by recovery.

## Provider and admission increment — 2026-09-09

`docs/COMPUTE.md` describes the provider seam and current launch boundary. Worker
execution, normalized lifecycle status, stop, and harvest now pass through the
provider interface; verifier/measurement execution uses the shared local process
implementation. Initial Studio Git intake still precedes provider preparation.
Provider-owned source preparation is separately implemented and tested through
re-prepare, execution, and protected-baseline harvest.

Applications may declare strict compute requirements. No registered provider can
currently satisfy those requirements; admission refuses them before runnable work
exists. Existing local apps record an explicit development exception with actual
unrestricted egress and absent workspace confinement. This is not sandboxed T1
admission, a T2 implementation, or either product gate.

Validation: race tests, vet, and whitespace checks pass. Provider tests cover failed
and timed-out stop/harvest, process-group shutdown, cleared environment, stream
offset replay, duplicate start refusal, unsupported snapshots/steering, changed
posture/identity refusal, and source-copy baseline persistence. Admission tests
independently weaken the T2 isolation, locality, network, data, confinement, and
metering requirements and verify refusal. Runtime tests prove the worker uses
the interface and logs the actual development posture.

A live built-binary CLI/HTTP check completed two checkpointed segments through the
provider path, retained all recovery evidence across restart, and recorded four
provider lifecycle statuses. A strict T2 application was refused before another
run was created. Deadline, source contents, and prior segment output were preserved.

## Autonomy measurement increment — 2026-09-09

`docs/AUTONOMY_METRICS.md` records the metric definitions and uncertainty rules.
Readers fold the log rather than trusting mutable run projections or harness
narration. Offline reads verify the chain without opening a writer. The report
preserves unanswered/defaulted distinctions, bounds censored horizons, retains
discarded work from unsuccessful runs, and avoids double-counting concurrent
productive intervals. Passing checkpoints require recorded commit and evidence
bindings; local passes still do not establish inaccessible-oracle verification.

New execution plans persist an unattended-grant assessment. The current provider
receives zero; measurement does not raise that grant. The historical corpus audit
in `docs/autonomy-baseline.json` finds 11 unsupported runs and no measurable rates.
Representative corpus coverage, policy/model/classifier evaluations, positive grant
admission, and both product gates remain unmet.

Validation: race tests, vet, and whitespace checks pass. Metric tests include real
recovery and offline/live equivalence, incomplete denominators, precision bounds,
default expiry, censored horizons, invalid clocks, post-completion events, concurrent
intervals, discarded unsuccessful work, ignored narration, and damaged/partial logs.
A built-binary live check returned identical CLI/API/offline metrics for one
accepted change, one human decision, three checkpoints, and one discarded segment;
the recorded unattended grant remained zero across restart.

## Fleet coordination and evidence increment — 2026-09-09

`docs/FLEET.md` documents declared path ownership, named singleton resources,
pre-dispatch acquisition, harvested-path enforcement, and explicit inspection of
claims left uncertain after a crash. Claims and conflict state replay from the
same log. Managed execution releases only after stopping; successful release is
part of the terminal event. A queued automatic restoration with held claims is
parked on restart and can resume its saved recovery after inspection without
consuming another restart or replacing the original deadline/memo.

The shared fleet CLI/API reader exposes bounded structured evidence, canonical
artifact references, numeric predicates, and a deterministic digest bound to the
current event prefix. Narration, raw event payloads, card text, and artifact bodies
are excluded structurally. It grants no cross-run mutation authority. The full
isolated/metred fleet capability, authenticated actor envelope, orchestration,
distributed fencing, and sibling integration task remain unimplemented.

Validation: `go test -race ./...`, `go vet ./...`, and `git diff --check` pass.
Tests cover path boundaries, disjoint concurrency, cross-workspace singleton
conflicts, unchanged conflict deduplication, out-of-scope harvest, cancellation,
crash-held claims, terminal inspection/replay, active-release refusal, and saved
rewind resumption at its restart bound. Evidence tests inject private narration
into output, event payloads, summaries, cards, and unknown enums; the DTO excludes
it while preserving predicates/references and replaying the same digest.

A live built-server smoke test submitted work through the CLI, verified HTTP/CLI
reader equality and conflict waiting, downloaded the retained out-of-scope patch,
and killed the server while a real worker was running. Restart retained its claims
and blocked conflicting work. The test inspected and stopped that worker's process
group, explicitly released the claims, observed the follower complete, and verified
both the unchanged source and replay-stable evidence after another restart.

## Task and pool dispatch increment — 2026-09-09

`docs/WORK_POOLS.md` records the new Task/drainer contract. Every local submission
has a durable Task, and dispatch now computes dependency readiness before selecting
by pool policy and acquiring ownership. A waiting run receipt is not an admitted
attempt: Task `runs` remains empty until dispatch. Task lifecycle snapshots and
named transitions are carried by synced Effects; terminal Task and Run completion
are published together. Historical logs receive labelled projections without
rewriting their events.

Implemented dependency support is cross-pool, same-local-tenant FS control edges,
terminal-status guards, soft priority hints, and all/any/quorum joins. Admission
records exact prerequisite event witnesses and the pool policy used. Recovery
retains those witnesses; a new attempt rechecks upstream state. Unknown tasks,
cycles, sample prerequisites, unsupported edge forms, stale selected policies,
and overcommitted reservations are rejected.

Validation: `go test -race ./...`, `go vet ./...`, and `git diff --check` pass.
The tests exercise dependency blocking before claims, task/run state separation,
retry lineage, frozen result bindings, priority aging, deadline ordering, earliest
race/quorum witnesses, pool reservations/concurrency, replayed turn order,
absolute deadline admission/execution limits, legacy projection, strict API
input, and shared console/API controls. Recovery tests also pass with lifecycle
changes recorded only as Effects.

A live built-server smoke test exercised ten tasks: a one-slot LIFO pool,
a queued deadline expiring without execution, cross-pool dependency blocking,
stdin delivery of the saved prerequisite witness, an any join, a failure branch,
and a retry unblocking a success-dependent task. Task state and pool policies
were identical after restart; CLI/API/offline autonomy reports matched, and the
source workspace remained unchanged. The full Studio/Market gates and the
remaining Work Pool mechanisms listed above remain outstanding.

## Artifact data-dependency increment — 2026-09-09

`docs/DATA_DEPENDENCIES.md` documents explicit artifact transfer through FS data
edges. Admission binds selected artifact names, SHA-256 values, and sizes to the
chosen producer result; required inputs are checked before ownership acquisition.
Optional unavailable inputs record an omission. Race/quorum losers do not supply
required bytes. Per-consumer bounds are 128 files / 64 MiB, with 1–32 names per edge.

Before each worker segment, Studio verifies the typed result witness, registration,
file type, actual size, and complete digest, then publishes separate synced copies.
The worker receives input-directory affordances and the retained manifest. Input
fingerprints include Git-named data. Changes during either worker execution or
verification withhold acceptance and park for judgment after preserving harvest.
Explicit recovery recreates copies from the same pinned producer; upstream retries
do not rebind an admitted consumer. This is a same-user development mechanism,
not an inaccessible read-only mount or an unattended-execution grant.

Validation: the full `go test -race ./...` suite passed, followed by focused input,
fleet, and admission regression checks for the final changes. `go vet ./...` and
`git diff --check` pass. Tests cover exact materialization, typed witness replay,
producer retry pinning, missing/optional inputs, size bounds, race selection,
corrupted bytes, symlinks, worker and verifier mutation, Git-named inputs, explicit
recovery, and CLI/form selection validation.

A live built-server smoke test used CLI selection to transfer two inventory
artifacts, compared their private copies with verified downloads, checked file
modes and stdin/manifest agreement, parked an input-changing consumer with its
patch retained, and failed a missing required input before dispatch. Producer
retry and server restart preserved the original consumer binding, source files
stayed unchanged, and live/offline metrics agreed. Typed generalized outputs,
other dependency relations, invoke, protected mounts, and the full launch gates
remain outstanding.

## Service composition and manual-trigger increment — 2026-09-09

`docs/SERVICES.md` describes the local project catalog and desired-set resolver.
Targets and explicit enables create mandatory demands; presets and wants create
optional demands. Explicit disables take precedence. Recursive requires bundles
avoid partial activation when a requirement is unavailable or an optional bundle
conflicts. Independent demands survive rejected bundles. Mandatory conflicts name
both demanders; before/after order active nodes without pulling inactive nodes in,
and active ordering cycles are rejected.

Catalog preview performs admission validation without mutation. Revision-checked
saves persist a standalone synced Effect; activation reasons remain derived. Each
active manual service has a validated installed-application Brief. Trigger keys
create one durable Task/Run receipt with source and catalog revision in the same
creation Effect. Duplicate keys return that receipt across configuration changes
and restart. Dispatch still applies ordinary pool, dependency, ownership, deadline,
acceptance, and recovery rules.

Validation: `go test -race ./...`, `go vet ./...`, and `git diff --check` pass.
Tests cover precedence, requirement failure, independent demands, missing wants,
mandatory/optional conflicts, ordering/cycles, deterministic catalog permutations,
unsupported runtime fields, invalid work bindings, preview without writes, stale
and unchanged saves, concurrent duplicate triggers, atomic provenance, deactivation,
exact replay, offline audit, and shared HTTP/form operations.

A built-binary live check completed a service-triggered inventory in the batch
pool, previewed and saved a catalog, verified conflict demander names and stale
revision refusal, and deduplicated repeated keys. Disabling the service refused a
new key while preserving the original receipt. Restart retained exact catalog,
reasons, Task, and deduplication state; CLI/API/offline metrics matched, and source
contents remained unchanged.

Desired activation is not running-process state. Configuration changes do not
stop already submitted Tasks, and ordering does not sequence independent manual
triggers. Automatic triggers, service health/breakers/restarts, part-of lifecycle
propagation, pipeline requirements, platform/tenant classification, and hosted
project isolation remain unimplemented. Both full product gates remain unmet.

## Interval scheduling and service backpressure increment — 2026-09-09

`docs/SERVICES.md` now covers automatic interval Tasks using the retained
`schedule.interval`, `jitter`, `run_on_startup`, and `concurrency` field names.
Catalog Effects initialize active clocks; unchanged schedules retain them through
unrelated edits. A due occurrence syncs its Task, service provenance, and next
cursor together. Capacity skips and admission rejections advance the cursor in
explicit standalone Effects. Downtime coalesces past-due intervals into one
occurrence; startup requests still obey capacity. Manual and scheduled receipt
keys occupy distinct namespaces.

Service max_instances and bounded skip/queue backpressure sit alongside pool
policy. Dispatch rechecks service capacity before claims. Parked Tasks retain
slots, and blocked Tasks expose the service wait reason. Disabling a service stops
future submissions while existing Tasks continue. Inactive bindings can be saved
when an app/workspace has disappeared or an absolute deadline has expired, allowing
operators to disable a failing schedule. Reactivation requires full validation.
The Services reader/UI exposes next/last occurrence, outcomes/reasons, submission,
skip/rejection counters, pending/admitted counts, and latest run status.

Validation: the full `go test -race ./...` suite passed. Focused race-enabled Task,
pool, service, interval, and concurrent-tick checks passed after the final dispatch
wait-reason and inactive-binding changes. `go vet ./...` and `git diff --check` pass.
Tests exercise clock bounds/jitter, early/duplicate/concurrent ticks, atomic
creation/replay, downtime coalescing, bounded queue skips, admission rejection,
pre-claim concurrency gating, parked slots, completion release, startup behavior,
manual-key separation, configuration clock preservation, disable/reactivation,
and offline audit compatibility.

A built-server live check automatically submitted two command Tasks, held the first
in execution, observed the second blocked before claims, and recorded subsequent
intervals as capacity skips. Disabling the schedule stopped future submissions;
releasing the first worker let both Tasks complete and verify. Reactivation with
a future interval and server restart retained exact schedule/runtime/Task state.
CLI/API/offline metrics agreed on two accepted changes, and source files were
unchanged.

Cron/event/continuous and external triggers, service health/circuit breakers,
restart policies, kill-on-overlap, service priority, lifecycle propagation,
pipeline roots, and hosted tenancy remain outstanding. Scheduled local execution
retains the existing provider posture and zero unattended grant. Both complete
product gates remain unmet.

## Service circuit-breaker increment — 2026-09-09

`docs/SERVICE_BREAKERS.md` records closed/open/half-open behavior using the retained
threshold, cooldown, and half_open_requests fields. Admitted execution failures
count toward the threshold; queued refusals, skips, parking, and cancellation do
not manufacture failed executions. Opening gates manual intake, interval occurrences,
and queued dispatch before claims. Existing receipt keys remain readable.

Admission binds each attempt to a policy epoch and recovery generation, and reserves
probe quota in the same Effect as scheduling/claims. Terminal Effects carry the
resulting circuit snapshot alongside the Run outcome. All configured probes must
pass to close; failure renews cooldown, and cancellation releases its reservation.
Parked recovery retains the reservation. Accepted retry closes the old probe and
releases its quota atomically without overwriting the new attempt's Task state.
Late outcomes from prior generations/policies cannot undo current protection.

Unrelated catalog edits and restart preserve cooldowns, failure counts, and probes.
Changing effective breaker policy starts a new epoch; disabling the breaker/removing
its unit removes the gate. CLI/API/UI expose state, failures, recovery time, and
probe progress. The reader and metrics folds consume the same saved Effects.

Validation: the full `go test -race ./...` suite passed, followed by focused race
checks for the final admission/replay guards and concurrent probe selection.
`go vet ./...` and `git diff --check` pass. Tests cover threshold/cooldown, stale
successes, multiple probes, renewed cooldown, cancellation, parked reservations,
accepted retries, policy epochs, queued wait reasons, manual idempotency, interval
skips, exact replay, per-run/offline metric equivalence, and shared UI/API behavior.
Concurrent admission confirms a one-probe limit despite available pool capacity.

A built-server check caused a real command failure, observed an open circuit and
scheduled skips, and verified manual refusal. Restart preserved the exact cooldown.
A bounded recovery probe then ran; excess probes were refused. Disabling future
schedule occurrences while that probe ran preserved its breaker, and its passing
acceptance closed the circuit. Another restart retained final runtime/Task state;
CLI/API/offline metrics agreed and source files remained unchanged.

Service health checks, restart/backoff policies, remaining trigger types,
kill-on-overlap, lifecycle propagation, pipeline roots, and hosted tenancy are
still outstanding. Circuit breakers do not change the local provider posture or
zero unattended grant. Both full product gates remain unmet.

## Service restart-policy increment — 2026-09-09

`docs/SERVICE_RESTARTS.md` describes retry/skip/abort, explicit zero retry counts,
and capped exponential backoff. Service admission persists policy and the root
retry deadline. Failure records its retry plan in the same Effect as the Run
outcome; consuming that plan and creating a child Run are also atomic. Each
additional attempt retains Task identity, source, dependency policy, and the
original deadline, and re-enters ordinary pool/claim/service/breaker admission.

Tasks remain nonterminal during pending backoff so failure-dependent work waits
for final exhaustion/closure. Pending retries reserve service capacity. Expiry,
admission refusal, activation/policy changes, manual supersession, and operator
cancellation close plans without duplicate attempts. Cancellation uses the shared
Run stop endpoint and closes the Task while preserving the failed Run as evidence.
Abort persists a per-service stop; unrelated services continue, and explicit
reactivation or changed policy can clear it. The console shows pending retry
controls, deadlines, abort state, and the latest attempt of the latest service Task.

Validation: `go test -race ./...`, `go vet ./...`, and `git diff --check` pass.
Additional focused race checks verify initial backoff cannot exceed the original
Task budget and skip/zero-retry policies cannot create new attempts. Tests cover
backoff growth/caps, fractional multipliers, atomic plan/child replay, concurrent
reconciliation, capacity reservation, bounded attempts, original deadlines,
retry-aware failure guards, cancellation, policy/activation changes, unavailable
apps, manual supersession, local abort/reset, breaker gating, and shared UI/API.

A built-server check restarted during pending backoff and preserved the exact due
time/deadline. The same Task recovered in its second attempt with that deadline
as its execution limit. Another Task exhausted its configured retry count before
releasing a real failure-dependent Task. Abort then withheld new service intake
and remained set after restart. Failure/plan and child/consumption records were
atomic; final Task/runtime state replayed exactly, CLI/API/offline metrics agreed,
and source files remained unchanged.

Health checks, remaining service trigger types, kill-on-overlap, lifecycle
propagation, pipeline roots, hosted tenancy, and the broader outstanding mechanisms
above still prevent either full product gate. Local execution retains its existing
provider posture and zero unattended grant.

## Calendar scheduling increment — 2026-09-09

`docs/SERVICE_CRON.md` documents the retained five-field numeric matcher and IANA
zones. All fields must match, including day-of-month and weekday; wildcard steps
retain value-modulo semantics. Parsing supports ranges/lists/steps with bounds and
rejects impossible day/month combinations. Calendar search spans a Gregorian
cycle. Timezone inversion skips nonexistent local times and includes both matching
instants in a fold, including half-hour transitions, in UTC order.

Preview includes calculated clock instants without mutation. Initial clocks are
now persisted with catalog Effects, and unchanged schedules preserve them. Replay
uses those recorded instants rather than a newer timezone database. Cron occurrence
keys use their UTC instant; Task creation and next cursor remain atomic. Startup
returns to calendar boundaries, downtime coalesces overdue work, and an unavailable
future match records a paused clock without blocking other dispatch. Existing
concurrency, retry, breaker, abort, and deadline rules still apply.

Validation: `go test -race ./...`, `go vet ./...`, and `git diff --check` pass. Tests
cover numeric grammar, retained conjunction/steps, impossible dates, leap/century
rules, rare leap-day/weekday combinations, non-hour offsets, DST gaps/folds,
half-hour changes, concurrent compilation, clock previews, saved-clock replay,
calendar-aligned catchup, bounded queues, startup, distinct fold occurrence keys,
paused unavailable calendars, and shared HTTP/UI/offline readers.

A built-server check waited for and executed a real cron calendar boundary,
verified the saved initial clock and cron occurrence record, and confirmed the
next cursor remained on the calendar. Restart preserved the future clock and Task
exactly. Disabling stopped the clock, an impossible-calendar preview was refused
without mutation, CLI/API/offline metrics agreed, and source files were unchanged.

Event/continuous/webhook/external triggers, service health checks, kill-on-overlap,
lifecycle propagation, pipeline roots, hosted tenancy, and the broader missing
mechanisms still leave both full product gates unmet. Cron scheduling does not
change the local provider posture or recorded zero unattended grant.

## Service outcome chaining increment — 2026-09-09

`docs/SERVICE_EVENTS.md` documents event schedules and inferred event hooks for
service Task success, failure, and completion. Exact and trailing-wildcard
patterns match active subscriptions at the terminal Task Effect; overlapping
hooks produce one delivery per recipient. Intermediate retry failures and
cancellations emit no final failure/completion event. Historical outcomes are
not matched when a subscription is enabled later.

The source terminal Effect saves delivery allocations and pending capacity
reservations. Consumption and child Task/Run creation share a second Effect.
Work bindings and normalized subscriptions are frozen and rechecked; changed
activation/subscriptions/work suppress pending intake, unavailable applications
reject it, and unrelated catalog edits preserve it. The child and its worker
stdin carry the exact source Effect sequence/hash, source service/Task/Run IDs,
root identity, and depth. Artifact transfer remains explicitly declared.

Feedback stops after eight descendant generations or 64 allocated deliveries
across all branches of one root. Reservations spend the shared budget permanently,
and retries preserve lineage. Source terminal capacity and breaker/abort updates
are projected before self-delivery allocation. Dispatch drains bounded batches;
ordinary pool, dependency, claim, and service gates still control execution.
Services UI/API expose delivery counts, source links, and closure reasons.

Validation: `go test -race ./...`, `go vet ./...`, and `git diff --check` pass.
Focused tests cover string/array grammar, inferred hooks, wildcard matching,
unsupported event refusal, overlapping names, concurrent reconciliation, pending
and consumed replay, subscription/work edits, no retroactive delivery, retry
settlement, cancellation, self-feedback, shared branching budgets, projected
breaker state, retry lineage, worker stdin, and shared HTTP/UI/offline readers.

A built-binary live check completed a three-service chain and delivered exact
source evidence to a real command consumer. A busy consumer recorded a capacity
skip; overlapping hooks created one receipt. A real self-feedback service stopped
at nine total Tasks (seed plus eight descendants). It verified terminal/allocation
and receipt/consumption atomicity, exact state after restart, idempotent manual
keys, equal CLI/API/offline metrics, and unchanged source files.

Custom/raw worker events, continuous/webhook/external triggers, health checks,
lifecycle propagation, distributed fencing, hosted tenancy, and the broader
missing mechanisms above still leave both full product gates unmet. This local
chaining increment retains the provider's zero unattended grant.

## Continuous service lifetime increment — 2026-09-09

`docs/SERVICE_CONTINUOUS.md` specifies the continuous controller. Desired activation
initializes its epoch and initial due time in the catalog Effect. A Task receipt
and controller ownership are created atomically, so queued, blocked, parked, and
retrying work retain one managed generation. Final Task completion saves the
restart delay in the same Effect. A subsequent generation receives a fresh Task,
Run, and workspace. Existing service concurrency and pool rules still apply.

Disabling/removing/changing the controller's schedule or work retires its current
Task. Catalog application fences queued old work before claims; reconciliation
uses the durable stop and harvest path, including cancellation of pending retries.
Unrelated catalog edits preserve ownership and saved delay. Manual cancellation
replaces the Task while desired activation remains enabled; disabling stops the
managed lifetime. Current parked recovery is allowed, while a relinquished old
generation cannot be resurrected by manual retry.

Replay preserves future delay and committed receipts. Downtime creates no backlog,
and interrupted running work is parked through the existing recovery contract.
Automatic retirement retains uninspected crash-held claims. Capacity, breaker,
and abort blocks retain a due generation without repeated unchanged log records;
invalid intake pauses with a reason until a binding/activation change. Services
UI/API/CLI expose the controller and its managed Task. Run pages explain
replacement and direct operators to service controls.

Validation: `go test -race ./...`, `go vet ./...`, and `git diff --check` pass.
Focused race tests additionally cover the final operator controls and crash-claim
retention. Checks cover schedule grammar/defaults/bounds, zero delay, atomic
creation/completion, concurrent reconciliation, saved-delay replay, late startup,
pre-claim retirement, removal and replacement, unrelated edits, cancellation,
capacity, unavailable apps, retry exhaustion, abort, breaker cooldown, parked
recovery, pending-retry disable, and live/offline metric agreement.

A built-binary CLI/HTTP smoke check ran a real continuous worker, completed its
first generation, preserved its restart delay across server restart, and started
a fresh second Task. The first completion triggered one downstream event Task.
Disabling stopped the second worker and retained its changed-file harvest. It
verified creation/ownership and completion/delay atomicity, exact disabled-state
replay, equal CLI/API/offline metrics, and unchanged source files.

Health/readiness probes, lifecycle propagation across related units, custom/raw
worker events, webhook/external intake, distributed ownership, hosted tenancy,
and the other outstanding product mechanisms still leave both full gates unmet.
Continuous work retains the current local provider's zero unattended grant.

## Service queue priority increment — 2026-09-09

`docs/SERVICE_PRIORITY.md` defines the retained low/normal/high/critical service
preference and its fixed v1 adjustments. Priority pools add that adjustment to
the unchanged base Task priority, ready-time aging, and soft-dependency score.
FIFO/LIFO/EDF and pool rotation keep their own ordering. Priority does not bypass
dependencies, service capacity, claims, breakers, or abort policy.

Manual, interval, cron, and continuous calls bind service ID, catalog revision,
priority, and adjustment atomically with Task creation. Event subscriptions bind
at delivery allocation and preserve that preference at later intake. Existing
receipts and retries retain the binding through catalog edits and replay. Legacy
Tasks and pending deliveries remain at zero adjustment. Current catalog priority
and each Task's bound preference are visible through shared CLI/API/UI readers.

Validation: focused priority/source-order race tests and `go vet ./...` pass.
The first full race invocation hit an existing five-second oracle fixture wait
while workspace preparation was still progressing. Three isolated race repeats
of that test passed without changes to its timeout or assertions. A subsequent
`go test -race ./...` passed without the concurrent smoke workload.
`git diff --check` passes. Coverage includes every registered trigger, enum
validation, explicit base priority, aging, FIFO/EDF behavior, service/dependency
gating, event-allocation revision versus intake revision, frozen retries,
legacy pending-delivery migration, replay, and shared HTTP/UI/offline readers.

A built-binary CLI/HTTP check held both real execution slots, enqueued service
Tasks in low/normal/high/critical order, and recorded actual admission in
critical/high/normal/low order when capacity became available. Changing the low
service's catalog priority while queued left its existing receipt unchanged.
Creation and preference evidence were atomic; state and idempotency replayed
exactly, CLI/API/offline metrics agreed, and source files remained unchanged.

Kill-on-overlap, preemption, health/readiness, lifecycle propagation, custom and
external intake, distributed fencing, hosted tenancy, and the broader missing
mechanisms still leave both product gates unmet. Queue preferences retain the
local provider's zero unattended grant.

## Kill-on-overlap service replacement increment — 2026-09-09

`docs/SERVICE_REPLACEMENT.md` defines bounded `if_running: kill` intake for all
registered triggers. At capacity, one replacement captures an exact prior
Task/Run/sequence/hash witness. Admitted work is preferred over queued work,
then oldest creation time and Task ID determine the target. Only one unsettled
replacement and one transient extra receipt/reservation are allowed per service.
A pre-existing backlog above a newly lowered limit drains before new intake.

Manual/timed/continuous receipts save intent atomically with creation. Events save
the target with delivery allocation, fence queued victim admission/retry, and stop
only after validated receipt creation. Suppressed allocations and cancelled
unstarted replacement receipts release their fences. A replay-derived reference
count index keeps these checks independent of historical queue size. Retired
continuous owners and expired queued requests cannot initiate a new stop.

Reconciliation uses the durable stop path, cancels saved retry backoff, and follows
the target Task's current attempt without targeting unrelated later service work.
A replacement waits for terminal Task state, all prior claim releases, and engine
worker-handle completion before admission. Unknown crash claims require inspection.
Run and Services pages link the old and new work; Task wait reasons distinguish
superseded work from a replacement awaiting shutdown. Stop summaries no longer
attribute automated service cancellation to an operator.

Validation: `go test -race ./...`, `go vet ./...`, and `git diff --check` pass.
The old interval-schema test was updated to reject an unregistered overlap mode;
positive coverage now accepts kill across manual/interval/cron/event/continuous.
Focused race checks cover atomic targets, capacity bounds, admitted-target
selection, rejected intake, concurrent/idempotent stopping, pre-claim fencing,
worker-tail gating, pending receipt and event replay, event suppression, withdrawn
stops, retained crash claims, saved retry cancellation, continuous retirement,
real process handoff, and shared HTTP/UI/offline evidence.

A built-binary CLI/HTTP smoke check ran an actual worker, submitted its replacement,
and recorded one stop Decision. It retained the old changed-file harvest and
proved the old cancellation Effect and claim release preceded new admission.
The replacement passed acceptance. Exact Tasks/runtime/metrics survived restart,
repeated keys returned the same receipt, CLI/API/offline metrics agreed, and the
source workspace remained unchanged.

Cross-pool preemption, health/readiness checks, related-unit lifecycle propagation,
custom/external intake, distributed fencing, hosted tenancy, and the remaining
mechanisms above still leave both full product gates unmet. Replacement retains
the same local provider posture and zero unattended grant.

## Service health increment — 2026-09-09

`docs/SERVICE_HEALTH.md` defines optional file, command, and heartbeat monitoring
for service command applications. Admission binds the effective policy; each
execution segment receives a fresh monitor ID. Root-confined file metadata checks
reject escaping links and future/stale timestamps. Command probes use the same
provider/posture and live workspace with separate home/tmp directories, capped
output capture, and a deadline bounded by five seconds, interval, and execution.
Only one probe per monitor runs at a time; its shutdown is joined before harvest.

Heartbeat CLI/API calls use server timestamps and increasing per-monitor sequences.
Duplicates do not refresh time or append events, and stale scopes are refused.
Current heartbeat evidence is rechecked under the store lock before applying an
expiry result or recording its stop decision. A concurrent fresh heartbeat can
withdraw obsolete expiry while monitoring continues. Each probe Decision, result
Effect, and stop Decision is durable; policy/execution changes suppress stale
actions. Run and Services pages expose monitor status, check evidence, and actions.

Unhealthy actions log, kill the current attempt without ordinary failure retry,
or request bounded service restart even when ordinary failure policy is skip.
Health retries preserve Task identity, retry count, backoff, and original deadline;
explicit abort takes precedence. Health failure bypasses checkpoint supervision.
Policy changes suppress pending health retries. Future continuous generations
still follow desired state. Restart parks uncertain execution and closes old
scopes; interrupted probes and uncertain actions are not automatically replayed.

Validation covers schema rejection/defaults, file confinement, log versus kill,
retained failed-work harvest, probe timeout, bounded health retries, heartbeat
deduplication/expiry and concurrent refresh, policy withdrawal, interrupted replay,
pending retry suppression, shared HTTP/UI readers, and live/offline metrics.
A successful health check cannot override failing installed acceptance.

`go test -race ./...`, `go vet ./...`, the CLI build, and `git diff --check` pass
on the final implementation. A built-binary CLI/HTTP smoke check stopped a real
worker after probe failure, saved its retry plan atomically, repaired the probe,
and completed a new attempt under the same Task and deadline. It also verified
worker stdin monitor metadata, heartbeat deduplication, old-scope refusal, rendered
health state, equal CLI/API/offline metrics, exact closed-state replay, and an
unchanged original workspace. Evidence for that final smoke run is at
`/tmp/mitos-health-smoke-bko236i4/result.json` in this development session.

Generalized builtin monitoring, readiness/dependency gates, lifecycle propagation,
custom/external intake, distributed fencing, hosted tenancy, and the broader
missing mechanisms remain outstanding. Health probes retain the local same-user
provider's zero unattended grant; they do not establish isolated oracle evidence.
Both full product gates remain unmet.

## Conditional service execution increment — 2026-09-09

`docs/SERVICE_CONDITIONS.md` documents the retained conjunctive `condition` fields:
file existence/absence globs, nonempty environment presence, equality/inequality
maps, and a shell command predicate. All registered triggers and builtin/command
applications share the gate. Queue admission saves the effective policy and
catalog revision atomically; a synced check Decision precedes evaluation and an
Effect records its result before application execution or acceptance can start.

File checks inspect source control files even when ignored by worker intake,
with root-confined lookup, bounded patterns and directory scans, and fail-closed
negative tests. Environment evidence records names and match results without
actual values. Command checks use a separate source copy and the application's
provider posture with cleared environment, private Git/home/tmp, capped output
capture, and the original execution context plus a five-second check deadline.
Their mutations do not enter the application workspace or result patch.

A mismatch or evaluation error preserves its receipt and cancels the attempt as
`service_condition_skipped`. It does not invoke the application or verifier,
request failure retry/abort, or emit success/failure service outcomes. Claims are
released; later independent triggers evaluate again, and continuous controllers
retain their restart delay. Admitted policies survive catalog edits. Interrupted
checks close on startup without command replay; explicit recovery evaluates the
bound condition again. Checks within an invocation are gates, not health monitors.
Run and Services CLI/API/UI readers expose current facts and outcomes, with prior
checks preserved in event history and understood by offline evidence readers.

Validation: `go test -race ./...`, `go vet ./...`, CLI build, and
`git diff --check` pass. Focused coverage includes all five registered triggers,
builtin gating, absence/presence and conjunction semantics, ignored source files,
environment value privacy, confined links and scan limits, private command
mutations, timeout, skip without application/acceptance/retry, atomic admission,
queued versus admitted catalog edits, pending-check replay, and HTTP/UI readers.

A built-binary CLI/HTTP smoke check returned an idempotent skipped receipt, then
accepted a fresh trigger after an ignored source control file appeared. The
command predicate and worker received no inherited test credential; actual server
environment values were absent from their evidence. The condition mutation stayed
in its private copy, the application passed acceptance, CLI/API/offline metrics
agreed, and Tasks/condition state/metrics replayed exactly. Source files were not
changed by execution. Session evidence is at
`/tmp/mitos-condition-smoke-1xsaa5zo/result.json`.

Intra-cycle service dependencies, lifecycle propagation, generalized readiness,
custom/external triggers, distributed fencing, hosted tenancy, and the other
missing mechanisms remain outstanding. The current local provider still has no
enforced oracle/credential isolation or positive unattended grant. The full Studio
and Market product gates remain unmet.

## Service completion dependency increment — 2026-09-09

`docs/SERVICE_DEPENDENCIES.md` defines the retained `depends_on` contract as
successful completion in the same scheduler cycle. The catalog rejects missing
unit references, duplicates, self-dependencies, and cycles independently of
activation and start-order relations. Dependencies do not pull in inactive or
not-due prerequisites and cannot use a service's historical last success.

Each new service Task atomically saves a cycle ID, catalog revision, service ID,
and frozen prerequisite declarations. Manual triggers share a caller-supplied
cycle key through CLI/API/forms. Due interval/cron occurrences and continuous
generations in one dispatcher pass share a cycle; event sibling deliveries share
the exact source outcome and allocation revision. A replay-derived index enforces
one Task per service per cycle. Trigger idempotency still returns its original
receipt, while retry attempts retain the existing Task's cycle membership.

The ready-set and claim paths require every matching prerequisite Task to be done.
Task all/any/quorum joins remain separate and cannot bypass this gate. Admission
saves exact prerequisite service/Task/Run/sequence/hash witnesses in the same
Effect as scheduling/claim acquisition. Already admitted invocations retain their
witnesses; new attempts recheck the prerequisites. Dependency waits consume an
absolute deadline bounded at receipt creation, preventing missing cycle members
from creating unbounded queued work. Existing failure/retry/deadline, service
capacity, replacement, breaker, ownership, and pool gates remain in force.

Run and Tasks pages expose cycle bindings; Run details link successful prerequisite
runs, and Services show declarations and manual cycle-key inputs. Worker stdin
includes the cycle and bound prerequisite evidence. Pending event allocations are
suppressed if their dependency declaration changes. Startup rebuilds cycle
membership without assigning dependencies retroactively to legacy receipts or
merging later scheduler passes into an interrupted one.

Validation: `go test -race ./...`, `go vet ./...`, CLI build, and
`git diff --check` pass. Focused race coverage verifies graph errors, old-cycle and
catalog-generation exclusion, direct-claim gating, atomic exact witnesses, Task
join independence, failed prerequisites and successful retries, admitted-witness
stability, duplicate-cycle refusal/idempotency, replay, timed/continuous cohorts,
event siblings, changed pending allocation suppression, and shared HTTP/UI readers.

A built-binary CLI/HTTP smoke check completed an old-cycle prerequisite, then
proved a higher-priority dependent remained unadmitted until its exact current-cycle
prerequisite succeeded. The admission Effect and worker stdin contained the same
success witness. Duplicate member intake was refused, original keys stayed
idempotent, source files remained unchanged, CLI/API/offline metrics agreed, and
Tasks/service views/metrics replayed exactly. Session evidence is at
`/tmp/mitos-service-dependencies-smoke-yf9yz9dz/result.json`.

Readiness gates, lifecycle propagation, custom/external intake, distributed cycle
coordination/fencing, hosted tenancy, and the broader missing mechanisms remain
outstanding. This increment retains the local provider's zero unattended grant.
The full Studio and Market gates remain unmet.

## Webhook and external-signal intake increment — 2026-09-09

`docs/SERVICE_INGRESS.md` defines local webhook paths and named external-signal
sources. Both create ordinary Tasks from the catalog's fixed work binding. HTTP
webhooks use delivery IDs from `Idempotency-Key`; signals use the explicit envelope
or `mitos service-signal`. Optional shared cycle keys participate in successful
service completion dependencies across ingress kinds in one catalog revision.
Reserved/noncanonical/ambiguous active webhook paths are refused.

Input is bounded UTF-8 JSON with duplicate-key, depth, value-count, and body-size
checks. Normalization preserves numeric tokens and sorts object keys. Creation
atomically saves source/kind/delivery identity, receipt time, normalized payload,
digest, and byte count in the service call and Task. Worker stdin receives the
same input under `service_ingress`; incoming fields do not select application,
workspace, command, or verifier. Run/Services pages expose delivery metadata.
Retries retain the original payload/provenance/cycle.

Concurrent identical redeliveries return the original receipt, and changed
source/content/cycle under a saved key is refused. Refused intake consumes no key.
The verified log rebuilds signal and route-level webhook identity indexes.
Original receipts remain readable after deactivation/removal and restart; new
deliveries require an active matching source/path. Existing capacity, replacement,
breaker/abort, conditions, health, dependency, ownership, and acceptance paths
remain in force. Receipt acknowledgement establishes durable enqueueing only.

Validation: `go test -race ./...`, `go vet ./...`, CLI build, and
`git diff --check` pass. Focused checks cover schema/routing, large JSON integers,
canonical equality, malformed/duplicate/deep/oversized JSON, concurrent duplicates,
changed-content conflicts, capacity refusal and retry, source matching, removed
route replay, shared cycles, retry provenance, fixed application execution,
worker payloads, HTTP/UI readers, and cross-origin refusal. HTTP intake explicitly
uses its 64 KiB limit while ordinary Studio forms retain their existing limit.

One initial focused process was inspected with SIGQUIT after remaining live without
output; its dump showed test PASS followed by race-runtime exit handling, not an
application wait. The diagnostic terminated that process. A clean focused repeat
and the full race suite subsequently completed successfully.

A built-binary HTTP/CLI smoke check delivered a webhook and a signal into one
completion cycle. It verified atomic input provenance and exact worker payloads,
large-integer precision, canonical duplicate receipts, changed-content refusal,
deactivated-route redelivery without new intake, unchanged source files, equal
CLI/API/offline metrics, and exact Task/service/metric replay. Session evidence is
at `/tmp/mitos-ingress-smoke-6dc2xpu9/result.json`.

Public webhook hosting, provider signature verification, queue transport clients,
upstream acknowledgement adapters, secret/tenant infrastructure, custom Effect
subscriptions, lifecycle propagation, readiness, distributed coordination, and
the broader missing mechanisms remain outstanding. Source names remain caller
claims matched to local configuration. The local provider still grants zero
unattended execution; both full product gates remain unmet.

## Custom named service events increment — 2026-09-09

`docs/SERVICE_CUSTOM_EVENTS.md` defines named JSON emission through
`mitos emit-event`, `POST /api/service-events`, and the Services form. Exact names,
trailing wildcards, and pattern arrays reuse the durable event delivery queue.
Reserved system names cannot be emitted by callers. The source is a global
`effect/custom_event_emitted`, with normalized payload evidence, catalog revision,
and initial subscriber allocations saved atomically. It does not invent a source
Task or application success, and an unmatched event is still a durable receipt.

Concurrent identical name/key submissions return the original source identity
and current recipient progress; changed content conflicts. Enabling subscribers
later does not rematch an old source, including on duplicate submission or replay.
Pending recipients reserve capacity and use existing subscription/work revalidation,
breakers, abort, replacement, and admission gates. Child receipt creation consumes
the allocation atomically. Direct subscribers share one completion-dependency
cycle, receive the exact payload in `service_event.custom`, and retain it through
Task retries. Their subsequent service outcomes retain the original root identity
and share its eight-generation/64-allocation limits across branches.

The CLI/API list the latest 50 source summaries without payloads; detail reads
return the payload once alongside recipient progress and Run links. Services and
Run pages link custom source receipts and display metadata. JSON intake retains
the 64 KiB envelope limit and number-preserving normalization; the HTML form uses
the existing 32 KiB encoded-form limit. Live and offline folds recognize the new
global Effect without attributing acceptance or human decisions to emission.

Validation: `go test -race ./...`, `go vet ./...`, CLI build, and
`git diff --check` pass. Tests cover concurrent identity, atomic allocation and
consumption, restart, capacity reservations, subscription suppression, no history
replay, reserved names, exact payloads, sibling prerequisite witnesses, shared root
budget/depth, HTTP validation/body limits, cross-origin refusal, UI/form readers,
and live/offline metric equivalence. The full race run completed with Studio at
123.304 seconds and Web at 9.137 seconds.

A built-binary CLI/HTTP smoke check emitted an unmatched event before catalog
configuration, then proved that its duplicate did not acquire later subscribers.
A new event completed two command subscribers with an exact same-cycle success
witness, atomic source/child records, precise worker input, canonical duplicates,
and changed-content/reserved-name refusal. CLI/API/UI evidence, CLI/API/offline
metrics, unchanged source files, and exact receipt/Task/service/metric replay all
passed. Session evidence is at
`/tmp/mitos-custom-events-smoke-a_x9n6f3/result.json`.

Raw worker/scheduler event producers, lifecycle propagation, readiness, pipeline
service roots, provider-specific connectors, distributed fencing, hosted tenancy,
and the broader missing mechanisms remain outstanding. Custom names remain local
caller claims. The local provider still grants zero unattended execution, and
both full product gates remain unmet.

## Explicit service stop and resume increment — 2026-09-09

`docs/SERVICE_LIFECYCLE.md` specifies explicit unit/target stops through
`mitos service-stop`, the lifecycle API, and the Services page. Stop follows reverse
`part_of` membership transitively, including targets, without changing configured
activation or pulling in services. Cycles and overlapping paths visit each node
once. The source Effect saves the catalog revision, exact member scope, and active
local Task identities; applying that boundary derives the same frozen affected
Task cohort and original Task-head witnesses during execution and replay.

All member admission holds are installed atomically before shutdown begins.
Manual, timed, continuous, event, webhook, signal, queued-dispatch, direct-claim,
and new-attempt paths respect them. Existing idempotent receipts remain readable.
Pending event allocations are suppressed, pending service retries are cancelled,
and existing work uses ordinary durable cancellation and command harvest. Drain
passes bound cancellation/delivery actions; already stopping workers are awaited.
Settlement requires terminal captured Tasks, worker exit, release of all attempt
claims, and closure of pending member deliveries. Startup reconciles pending
stops before service intake. Crash-held claims still require explicit inspection.

`mitos service-resume` releases only the root's settled operation. Independent
roots retain independent holds on shared members. Catalog removal or membership
edits cannot erase a captured stop or strand its release. Duplicate stop/resume
keys retain their original operation across removal, resume, and restart. Resume
restores normal triggers; it never resurrects cancelled Tasks or suppressed events.
Timers advance skipped occurrences normally, and continuous work retains its
saved generation timing. CLI/API/UI readers expose recent operations, full member
scope, total Task counts, and bounded original Task/Run/head witnesses.

Validation: `go test -race ./...`, `go vet ./...`, CLI build, and
`git diff --check` pass. The full race run completed with Studio at 119.916 seconds
and Web at 10.250 seconds. Focused tests verify reverse membership and cycles,
atomic holds, direct-claim refusal, concurrent idempotency, exact witness replay,
unrelated-work preservation, overlapping roots, removed-root release, pending
outbox suppression, future event refusal, worker/claim settlement, crash-held
claims, continuous/retry/timed/ingress behavior, HTTP/UI/form readers, cross-origin
protection, real command shutdown/harvest, and live/offline metric equivalence.

A built-binary CLI/HTTP check stopped running and claim-blocked queued work through
one target, verified saved output/change evidence and worker/claim settlement,
then exercised overlapping roots and catalog removal. Holds and all Task/service/
lifecycle/metric views replayed exactly. Resuming the removed root permitted fresh
work while cancelled Tasks stayed cancelled. CLI/API/offline metrics agreed and
source files stayed unchanged. Session evidence is at
`/tmp/mitos-lifecycle-smoke-0dkzbrsn/result.json`.

Atomic service restart, automatic propagation from desired deactivation or catalog
reconfiguration, generalized readiness/start-order barriers, pipeline service roots,
raw worker/scheduler producers, distributed fencing, hosted tenancy, and the broader
missing mechanisms remain outstanding. This increment retains the local provider's
zero unattended grant; both full product gates remain unmet.

## Explicit service group restart increment — 2026-09-09

The lifecycle API, `mitos service-restart`, and Services UI now support a durable
restart request over the captured `part_of` group. Old work uses the existing
shutdown/harvest/claim settlement path. Fresh active-member Tasks share a restart
completion cycle and retain exact source sequence/hash/root evidence in their
receipts, Task state, and command stdin. Continuous services retain ownership of
the fresh generation. An existing settled stop or failed restart can be replaced
atomically; its old receipt remains readable as superseded.

Preparation is bounded to 64 receipts per pass. The restart hold prevents any
prepared Task from executing until every receipt exists and one publication
Effect releases the group. Partial preparation replays without duplicate Tasks.
Policy/work/activation changes, invalid preparation, expired request-bound
deadlines, or cancellation discard the prepared cohort and retain a failed stop.
Independent stop holds remain in force; overlapping restarts are refused to avoid
mutual waits. A stop request can cancel a pending restart. Completion prerequisites
outside the restart cohort are refused before shutdown. Duplicate keys never
initiate another restart.

Focused race tests cover concurrent identity, replacing a settled stop, exact
same-cycle success witnesses, 65-member preparation/replay, direct-claim barriers,
partial-group rollback, overlapping holds, operator cancellation, continuous
ownership, old worker exit, and out-of-group prerequisite refusal.

Automatic configuration-driven lifecycle propagation, generalized readiness and
start ordering, pipeline service roots, raw worker/scheduler producers, distributed
fencing, hosted tenancy, and the broader missing mechanisms remain outstanding.
The local provider still grants zero unattended execution; the full Studio and
Market gates remain unmet.

Wrap-up validation: `go test -race ./...`, `go vet ./...`, CLI build, and
`git diff --check` pass. The final race run completed with Studio at 142.700 seconds
and Web at 11.637 seconds. A built-binary CLI/HTTP smoke check stopped an executing
worker, published fresh dependent Tasks, verified exact prerequisite and worker
source evidence, checked UI/API readers and duplicate identity, restarted the
server, and confirmed exact Task/service/lifecycle/metric replay. CLI/API/offline
metrics agreed and the original source remained unchanged. Session evidence:
`/tmp/mitos-wrap-smoke-lmha5_2f/result.json`.
