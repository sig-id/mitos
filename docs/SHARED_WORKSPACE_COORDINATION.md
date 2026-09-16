# Hosted sessions and shared workspace coordination

Design proposal · 2026-09-16 · not implemented functionality.

Implementation update: [the local workspace increment](INTERACTIVE_WORKSPACE.md)
ships a subset of this contract. Consult that document for actual behavior;
snapshot-based build reuse and complete mediated attribution remain targets.

Companion to [the product redesign](ORCHESTRATION_REDESIGN.md). The target use
case is five agent sessions editing different modules of one monolithic codebase
in the same directory. They discover relevant topics, share eligible build work,
see the effects of their commands, and avoid removing resources others need.

## Product contract

The hosted CLI remains recognizable. Show its actual interaction, tools, edits,
commands, streaming output, pending input, and supported controls. Mitos adds a
project coordinator, topic board, and a shared view of workspace activity. A
user can inspect the evidence behind a summary without opening a separate report.

The same facts must be queryable by agents with bounded responses. Useful tools
include topic discovery/following, active operations, file provenance, artifact
consumers, resource usage, and cleanup eligibility. These are proposed operations,
not existing CLI commands. Human and agent clients use one authoritative service.

Three distinct mechanisms cooperate:

| Mechanism | Responsibility |
|---|---|
| Topic board | Intent, questions, agreements, and progressive discovery of details. |
| Runtime registry | Current operation ownership, resource leases, dependencies, artifact lifetimes, and admitted actions. |
| Provider observation | Evidence about processes, filesystem changes, resource usage, and coverage gaps. |

A topic can link to a build or publish a contract. It cannot acquire a lock by
saying that a build is running. Observation can reveal an unexpected write, but
seeing the write afterward does not prevent it.

## Progressive discovery

The initial topic listing returns ID, title, short summary, affected paths/tasks,
status, revision, and last-update cursor. An agent filters by its assignment,
opens a relevant topic, and follows it. Subsequent reads return bounded deltas;
large output, diffs, and specifications remain referenced artifacts. Summaries
carry source revisions and can be expanded into original posts.

The coordinator curates topics and settles or escalates unanswered questions.
Runtime-generated notices about builds and resource contention are coalesced;
individual filesystem events do not become board messages or model context.
Changes to a bound contract or permission trigger a small mandatory notice or
gate for affected tasks even without a voluntary subscription.

A topic subscription saves a cursor and delivery state. Reading, acknowledgment,
and accepting an agreement are separate actions. Durable post IDs and causal
references prevent replay from executing the same requested action twice.

## Identity and attribution

Track the following causal chain where the provider can establish it:

```text
project → workspace → agent session → task attempt → tool invocation
        → operation → process descendants → observed effects and artifacts
```

A “thread” here is an agent conversation. It is not a kernel thread ID. Allocate
operation identity before launching the command and bind it through the trusted
adapter/provider. PIDs alone are insufficient; include provider/sandbox identity,
process lifetime identity, and parentage. Detached descendants retain their
provenance. A background daemon serving several clients needs request correlation;
its original launcher alone cannot identify the author of every later effect.

Shared jobs have an initiator and multiple consumers. Record their actual resource
use once, then expose which sessions benefited. Do not charge the full CPU time
or disk footprint independently to all five agents.

| Subject | Record |
|---|---|
| Command | Initiator, tool invocation, working directory, executable, relevant configuration, start/exit, descendants, output references. Protect credentials in arguments/environment. |
| File effect | Filesystem identity, path at the time, create/write/rename/delete, attributable operation, before/after content references when available, observation sequence and coverage. |
| Git operation | Actor, repository and worktree identity, operation, affected refs/index, before/after state, outcome, included patch or commit references. |
| Workspace/branch | Creator, source revision, attached sessions, current refs, outstanding changes, retention and removal state. |
| Artifact | Producer, category, location/identity, generation, allocated/logical bytes, consumers, pins, last use, retention policy. |
| Resources | CPU time/rate, memory, I/O, disk allocation, process count, pressure and observation timestamp at the available granularity. |

Separate process provenance, authorship of an edit, and authorship recorded in
Git. A commit made by one agent can include changes from several agents. Do not
rewrite Git author identity to pretend it records all contributors. A final diff
also cannot assign lines reliably when concurrent writers modified the same file.
Show known edits and mixed or unknown attribution explicitly.

Observe Git through supported tool invocation and optional Trace2 telemetry,
then reconcile actual refs, index, worktrees, and changes. Trace2 is useful
telemetry, not a compulsory mediation boundary; other libraries or direct file
operations can bypass it. [Git Trace2](https://git-scm.com/docs/api-trace2).

## Shared directory semantics

Give agents declared edit scopes but allow a common working directory. Track
repository-common resources separately from per-worktree resources. Linked Git
worktrees have separate HEAD/index state while sharing other repository state.
[Git worktrees](https://git-scm.com/docs/git-worktree/2.54.0).

Within one directory, branch checkout, reset, stash, index mutation, and commits
affect other sessions. Admit these through coordinated operations with an explicit
affected scope. A commit should have an exact selected change set; do not let an
agent's blanket staging silently collect everyone else's unfinished edits.
Source-wide transitions need a barrier that excludes conflicting writes and
operations. A single user's session can still inspect and request these actions.

Shared mode is cooperative until the provider mediates the affected operations.
Wrappers and agent instructions improve behavior but are bypassable. Display the
actual enforcement level. Unregistered processes touching the checkout remain
visible as external/unknown participants; do not silently attribute them to a
nearby session. Observation gaps invalidate claims of exclusive ownership.

Keep the dependency graph and combined verification step in shared mode. Workers
editing disjoint files can still introduce incompatible contracts. Separate
worktrees remain an available mode, with explicit integration of their outputs.

## Five agents requesting Cargo builds

Cargo already uses directory locking for build output. Mitos should add shared
job discovery, result delivery, correct input attribution, and resource scheduling.
Do not claim to invent Cargo's existing mutual exclusion.
[Cargo output layout and locks](https://doc.rust-lang.org/stable/nightly-rustc/cargo/core/compiler/layout/index.html).

Use a registered Cargo adapter rather than treating arbitrary commands with the
same text as interchangeable. The adapter submits an atomic “start or join”
request. The runtime checks two different identities:

- **Resource identity:** which build/cache/output directories and other singleton
  resources this invocation uses. Incompatible requests may have to serialize.
- **Result identity:** the exact input state and configuration whose result can
  satisfy a consumer. Only compatible, explicitly shareable requests can join.

Result identity must cover a stable source snapshot including relevant uncommitted
and untracked inputs, manifests/lockfile/config, toolchain, packages, target,
profile, features, flags, relevant environment and declared external inputs.
HEAD alone and the string `cargo build` are insufficient. Cargo build scripts
can depend on external state or have effects; when the adapter cannot account for
those inputs/effects, serialize execution without promising reusable equivalence.
Eligibility is conservative and independent of an agent's assertion that two jobs
are “the same.” Cargo remains responsible for its own incremental cache behavior.

An example interaction, using illustrative IDs:

```text
API agent:      cargo build → started build-42 on snapshot S17
UI agent:      equivalent request for S17 → joined build-42
Tests agent:   equivalent request for S17 → joined build-42
Worker 4:      request includes later edit S18 → queued for S18
Worker 5:      cargo clean → waiting for build/cache consumers
```

The running job provides one output stream and completion record to its consumers.
The UI states “Joined API agent's build” and identifies the validated snapshot.
An agent withdrawing its request removes that consumer; it does not kill a shared
job that still has consumers. Stop and cancellation authority belong to the job
policy and remaining ownership, with process settlement before resource release.

**Mutable source is the central constraint.** Prefer building a stable snapshot
captured with a consistent revision/barrier while agents continue editing the
shared directory. The command receipt shows its execution directory and snapshot;
do not silently redirect a command if that changes its promised output paths or
side effects. An agent requiring live-directory behavior can run that mode with
explicit limits on result reuse.

For a live-directory build, source changes during execution make the result cover
an uncertain input state unless a provider-enforced read barrier excludes writes.
Before/after hashes alone miss transient changes. Without sufficient evidence,
show “inputs changed / coverage unknown” and schedule another check; never claim
that later edits passed. A mutation generation helps invalidate results, but is
not a substitute for a consistent snapshot and known observation coverage.

Shared caches are separate from result identity. Namespace incompatible toolchain
and configuration outputs and let the tool maintain its locks. Cargo supports
configuring target and build output locations.
[Cargo build cache](https://doc.rust-lang.org/cargo/reference/build-cache.html).

Persist start/join intent, job state, consumer membership, and resource ownership.
On restart, reconcile provider jobs before launching replacements. A lease timeout
does not prove that a compiler stopped. Fence or terminate the old operation before
allowing a new owner to modify its protected resources. Order multi-resource claims
to prevent deadlock and bound waits with an inspectable reason.

## Artifact lifetime and cleanup

Distinguish source changes, generated source, build outputs, reusable caches,
temporary scratch, and retained evidence. An ignored file is not automatically
garbage; a generated file can still be an input or deliverable. Track creation
and later use, including shared consumption and ownership transfer.

Private scratch defaults to a session/operation directory. Shared caches and
build outputs belong to the project/job with consumer references. Branches and
worktrees are resources with users and retention state, not anonymous litter.
Policies can specify last-use windows, size limits, and retention after failure.

A cleanup proposal shows paths/resources, producer, consumers, retention reason,
and estimated reclaimable bytes. Admission takes an exclusive cleanup claim,
prevents new managed users, revalidates identities and pins, and waits for active
operations or readers to settle. Perform deletion through a provider operation
that respects this exclusion. If external users or observation gaps prevent that
guarantee, defer automatic cleanup and report why.

Open handles are useful evidence but insufficient by themselves: a process may
reopen a path later or retain a mapping, and checking handles before deletion
creates a race. Track intended use through leases/pins as well as observed handles,
mapped files, working directories, and descendant processes where supported.
Unlinking a still-open object may also fail to reclaim the expected space.
Never infer “safe to remove” solely from an empty sampled handle listing.

Disk views distinguish logical size, allocated size, shared/reflinked or hardlinked
storage, pinned bytes, and estimated reclaimable bytes where measurable. I/O byte
counters are not retained disk usage. Resource pressure can delay a new build or
request cleanup of eligible scratch, with the reason visible to the agents.

## Provider contract and observation coverage

Nucleus remains the intended integration described in the specs. OCI packaging
alone establishes neither per-session attribution nor the needed enforcement.
Describe capabilities through the provider seam, then validate its real event and
control surfaces. Do not assume a host PID equals a sandbox guest process.

For gVisor-backed execution, runtime monitoring can stream syscall and lifecycle
trace points to an external observer. This is a candidate input for provenance;
it does not establish that the installed Nucleus API exposes them, that all needed
events are enabled, or that the stream can block operations.
[gVisor runtime monitoring](https://gvisor.dev/docs/user_guide/runtimemonitor/).

For native Linux execution, inotify detects changes but does not identify the
responsible process. Fanotify can provide process information in supported modes,
but has permission constraints, queue loss, and coverage limitations, including
memory-mapped changes. Neither alone is a complete attribution or cleanup system.
[inotify](https://man7.org/linux/man-pages/man7/inotify.7.html) ·
[fanotify](https://man7.org/linux/man-pages/man7/fanotify.7.html).

Cgroup v2 supplies process-group resource accounting and control. With supported
delegation, map sessions or operations to separate groups for CPU, memory and I/O
observations. If several agents are in one opaque sandbox, report aggregate
measurements unless the provider supplies finer attribution; do not invent a
per-agent breakdown. Measure filesystem allocation separately.
[Cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html).

Capability negotiation should separately identify interactive input/output,
structured session events, process identity/lineage, filesystem actor coverage,
resource accounting granularity, snapshots, shared mounts/coherence, operation
mediation, and cleanup exclusion. Scope coverage by resource and time interval.
Record dropped events and reconnect gaps, reconcile state, and suppress automatic
reuse/cleanup when their evidence is incomplete. Complete syscall logging of every
build is not the default: retain bounded operation summaries and artifact manifests,
with raw detail subject to retention and privacy controls.

## What exists today

The checked Go implementation runs installed command applications with fixed
stdin and captured output. `internal/compute/local.go` supports copied workspaces
and rejects in-place execution. `internal/observation/contract.go` declares no
interactive input, PTY control, or resume. Nucleus is an external target in the
specs; no registered strict provider is implemented here.

Existing foundations include Task/Run lineage, private Git history and harvested
patches, declared path/resource claims, dependency artifacts, process-group stop,
inotify activity counts, and sampled process-tree observation. They do not yet
provide file-author attribution, shared live checkouts, hosted interactive CLIs,
topic subscriptions, reusable shared build jobs, or resource-aware cleanup.

Orca documents local agent-tool line attribution and has resource-manager and
workspace cleanup surfaces. The reviewed documentation/source does not establish
the complete coordinated build and artifact-lifetime contract proposed here; this
is not a claim that Orca lacks every constituent feature.
[Orca attribution](https://www.onorca.dev/docs/review/attribution) ·
[Orca workspaces and cleanup](https://www.onorca.dev/docs/model/worktrees).

## First implementation slice

Ship one real hosted CLI session with live output and supported controls. Then
connect a provider-backed identity/observation adapter, a small topic board, and
one managed Cargo operation to a shared project directory. Keep unknown activity
visible while making only the coordination claims the provider can enforce.

The first integration exercise should demonstrate:

1. Five sessions edit separate modules and discover only relevant topic details.
2. Equivalent builds against one stable input start once and deliver one result
   to all consumers; a changed input or feature set receives separate treatment.
3. A source edit during a live build prevents a false “your change passed” claim.
4. File/Git/process provenance links to the initiating session, with unknown or
   shared effects labeled accurately; direct bypass activity is not hidden.
5. Cleanup and `cargo clean` wait while artifacts are pinned or jobs are active;
   one waiter cancelling cannot destroy another waiter's build.
6. Restart preserves ownership and consumers without duplicate launch, and lost
   observation disables dependent automatic reuse or deletion.
7. CPU/memory/I/O/disk views report their measured scope and avoid double-counting
   shared work. The user can open the command and evidence behind a displayed fact.

This delivers useful coordination even before model-driven orchestration. Add the
visible project coordinator on those same tools; its decisions should build on
reliable runtime state rather than having to infer it from every agent's transcript.
