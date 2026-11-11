# 01 – Execution architecture and subsystem contracts

Status: Normative subsystem contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

This document defines detailed execution and packaging contracts. The
[OS architecture](AGENT_OPERATING_SYSTEM.md) owns primitives and layers; the
[durable runtime](DURABLE_RUNTIME.md) owns shared lifecycle, authority, receipts,
waiting, and recovery. Those contracts take precedence on their subjects. Existing
Decision/Effect and application fields below MUST remain compatible until a
versioned migration explicitly changes them. [The catalog](../README.md) resolves
numeric specification references.

RFC 2119 keywords (MUST / SHOULD / MAY) are normative.

**Implementation update (2026-09-08):** Go + HTMX replaces BEAM.
[Implementation status](IMPLEMENTATION_STATUS.md) records what is implemented and supersedes historical
language-specific callback syntax in this target contract.

---

## 0. Scope & project boundary

Mitos is the Go runtime for durable work. It owns authoritative records,
admission, scheduling, lifecycle, grants, resources, control messages, verification
bindings, and system services. Agentic policy and deterministic executors use the
same admitted operations. Browser, CLI, desktop, and handheld interfaces are clients.

| External seam | Responsibility |
|---|---|
| Harness / process host | Execute and inspect an exact managed incarnation; transport terminal or structured output. An external engine MAY implement this seam. |
| Sandbox provider | Enforce the declared filesystem, network, resource, and oracle boundary. Nucleus, containers, or remote VMs require actual integration and admission evidence. |
| Identity provider | Assert remote/multi-tenant principals. An integration such as sigid MAY supply identity; current trusted-local identity remains explicitly scoped. |
| Relay / notification transport | Carry authenticated connections or delivery hints without owning task authority. |

A transport implementation MUST NOT determine completion or permissions. Local,
remote, and hosted deployments MUST preserve the same conceptual contract while
advertising their actual capabilities. Optional commerce is governed by `08`.

## 1. Architectural requirements

1. **Durable work, replaceable executors.** Tasks own assignments; Services own
   ongoing responsibilities. Existing Runs are Attempts. Process and Session
   identities MUST be explicit, with incarnation fencing and preserved lineage.
2. **Agentic policy, deterministic admission.** Models MAY propose plans and
   actions; runtime code MUST validate authority, revisions, claims, and budgets,
   durably record accepted intent, and settle effects.
3. **Enforced boundaries.** Prompts MUST NOT substitute for confinement, grants,
   revocation, resource limits, or inaccessible verification. Unsupported strict
   requirements MUST be refused rather than silently weakened.
4. **Executor independence.** LLM harnesses and deterministic programs MUST share
   lifecycle and evidence contracts. Capability packages are reusable behavior,
   not the universal identity of work and not implicit authority.
5. **Common runtime, multiple clients.** Interfaces MUST use the same operation
   identities, receipts, and authorization. Client disconnect MUST NOT cancel work.
6. **Recovery before repetition.** Pending operations MUST be reconciled by their
   identity and effect class. Missing receipts MUST NOT imply that external effects
   did not happen. Unknown outcomes remain explicit.
7. **Verification independent of narration.** Only authorized acceptance bound to
   the exact result MAY satisfy verified dependencies. Worker success is a report.
8. **Optional application policies.** Studio and Market MAY share execution and
   evidence mechanisms with different policies. Payments and hosted tenancy MUST
   NOT be prerequisites for core local runtime conformance.

## 2. Two kinds of computation

| Deterministic (mitos owns) | Agentic (delegated) |
|---|---|
| run lifecycle, the event log, resume/replay | interpreting intent, planning |
| budget/limit enforcement, capability grants | generation, critique, repair |
| sandbox prepare/start/harvest, egress brokering | choosing which tools to invoke |
| trace + artifact persistence, metering | proposing the next pipeline move |
| service scheduling, memory storage/retrieval | proposing memory updates |

The deterministic layer does not try to make the model predictable. It makes the
model's **effects** inspectable, bounded, replayable, recoverable, and billable.

---

## 3. Layer stack

The authoritative layers are kernel, system services, and applications/clients
in [the OS architecture](AGENT_OPERATING_SYSTEM.md#7-layers-and-implementation-boundary).
A service activation creates a durable Task. Scheduling admits an Attempt and
Process under grants and claims. The executor MAY run a deterministic command or
an agentic pipeline. Pipeline orchestration and loop management below are system
service/application policies; they MUST NOT own the kernel or bypass admission.

The home authority persists decisions, receipts, state and outboxes. Execution
hosts own admitted process instances and provider enforcement. Clients inspect
state and submit requests. A relay or terminal renderer owns neither execution
policy nor authoritative task state.

## 4. Control model – agentic policy, deterministic record

The orchestrator never mutates state directly. Each step it emits a **Decision
event** (§8.1); the substrate executes the chosen action through deterministic
mechanisms and emits resulting **Effect events** (§8.2). The append-only,
ordered union of Decision + Effect events **is** the run state and the source of
truth.

```
state(t) = fold(events[0..t])
resume   = fold all committed records; reconcile each pending operation; resume authorized policy
```

**Crash test (must pass):** if execution dies after an admitted Decision and
before its Effect is recorded, recovery MUST inspect the operation's identity and
receipts. It MAY re-drive only when the operation is proven idempotent or safe to
repeat; otherwise it MUST reconcile or retain an unknown outcome. A later unrelated
Effect MUST NOT hide an earlier pending Decision. See [recovery](DURABLE_RUNTIME.md#8-recovery-and-external-effects).

**Normative invariants (load-bearing – violating any defeats the architecture):**

- The event log is the **only** source of truth for run state. Processes MAY
  cache a fold; they MUST NOT own state the log cannot reproduce. Any answer
  returned to a caller MUST be recomputable from `fold(events)` with no process
  running.
- A new `Effect` subtype MUST be consumed by every fold reader in the same
  change – in mitos: `Run.State`, `Meter`, and `Proof` (`08 §2`). A reader that
  silently ignores a new Effect is a defect, never a deferral.
- Redaction is enforced at the **data layer**, never the view layer: a
  commercial cut is a fold over a strict Effect subset; `rationale` and harness
  identity are physically incapable of reaching it (`08 §2`, `11`).

The packaged agentic pipeline has two control responsibilities; this does not
limit the number of managed supervisor/worker Processes in the OS:

- **Orchestrator** (cross-step): adapts the pipeline, handles stuck/edge cases,
  routes (dispatch is folded in). One agent.
- **Loop-manager** (intra-step): a deterministic FSM that composes the next
  iteration's context and only spends a model call to decide an escalation or a
  model/effort/harness switch. (Replaces chief-wiggum's dumb summarize-and-loop.)

---

## 5. Capability packaging – see `03_CAPABILITY`

| Pillar | What | Where |
|---|---|---|
| **SKILL** | formalized markdown know-how | in the OCI image; `resolved = base ⊕ per_model_adapt(model)` (seam only; generator out of scope) |
| **TOOLS** | deterministic CLI/Python/JS | in the OCI image |
| **DATA** | persistent refs/corpus/DB (NOT the input workspace) | RO provider snapshot/volume |
| *harness* | the coding agent | **injected by mitos at launch** from a compatible set → harness-switchable mid-step |

A capability emits a **soft result + evidence** (§8.3), never a rigid control
token. The orchestrator interprets `result + evidence + trace`.

---

## 6. Trust tiers

| Tier | Who | Sandbox | Egress | Data |
|---|---|---|---|---|
| **T0** | first-party/system capabilities (orchestrator helpers, verifiers) | Nucleus (gVisor) | local broker / unix socket | bind/RO |
| **T1** | local user on own code | Nucleus / Docker | local broker | bind |
| **T2** | **untrusted tenant/seller capability on buyer data** | **remote µVM (E2B/Daytona)** | **broker-only egress, deny-all-else** | RO snapshot |

Trust tier is an input to the Tier-1 launch policy compiler and selects the
eligible provider set + network/mount posture.

---

## 7. Runtime ownership

The kernel owns admission, state, receipts, scheduling, grants, and claims.
Supervision, verification, memory retrieval, and credential brokering are system
services. Host/provider mechanisms enforce execution boundaries. An optional
external engine MAY implement transport and process-host operations; integration
MUST preserve exact launch identity, inspection, and result receipts.

Raw terminal output MAY stream to authorized clients under the
[terminal contract](TERMINAL_STREAMING.md). The observation policy in `14` governs
what a model may consume as evidence or use for verified steering; it MUST NOT be
interpreted as a ban on human-facing terminal streaming. Terminal bytes and
agent-authored messages alone MUST NOT establish verified completion.

## 8. Execution and packaging contracts

Subsystem specs elaborate these fields. New durable-runtime bindings MUST be
introduced through versioned schemas without silently reinterpreting existing
fields. The current local JSON dialect remains defined by its local contracts.

### 8.1 Decision event (orchestrator / loop-manager → substrate)

```
DecisionEvent {
  run_id      : string
  seq         : u64                 # monotonic per run
  ts          : iso8601
  actor       : "orchestrator" | "loop_manager" | "supervisor"
  type        : advance | insert | retry | switch | skip
              | request_approval | escalate | finish | abort | rewind
  target      : capability_ref | stage_id | checkpoint_id | null
  params      : object              # type-specific (e.g. switch → {model,effort,harness})
  inputs_hash : string             # hash of the state the decision was taken on (replay integrity)
  rationale   : string             # NL justification – AUDIT ONLY, never controls flow
  budget      : { tokens, usd, wall_ms, remaining }
}
```

### 8.2 Effect event (substrate → log)

```
EffectEvent {
  run_id, seq, ts,
  type      : capability_started | capability_finished | tool_brokered
            | egress_allowed | egress_denied | artifact_registered | commit
            | approval_requested | approval_resolved | memory_written | budget_charged
            | task_enqueued | task_blocked | task_done | invoke_requested | subtask_returned
            | quiescent | steer_applied | steer_rejected | harness_quarantined     # 14
            | decision_delivered | decision_defaulted | decision_rejected          # 15
            | checkpoint_taken | oracle_tampered                                   # 16
            | ...
  caused_by : seq                   # the DecisionEvent that led here (causality)
  ref       : object                # ids / paths
  payload   : object
}
```

The **event log** is the append-only ordered union of Decision + Effect events.
It is simultaneously: run state, debug trace, replay source, audit record, and
billing ledger (`08`).

### 8.3 Capability manifest (skeleton – elaborated in `03`)

```
name, version
image        : <oci-ref>                     # SKILL + TOOLS baked in
data         : { snapshot: <ref>, mount: "/data", ro: true }
harnesses    : [claude-code, codex, ...]      # compatible set; mitos injects one
skill        : ./skill.md                     # base; resolved = base ⊕ per_model_adapt(model)
tools        : [ {name, schema, ...} ]
trust_tier   : T0 | T1 | T2
resources    : { cpu, mem, timeout }
egress_policy: [ {dest, credential, requires_approval} ]   # consumed by the broker (§8.5)
result       : { schema, evidence: [path|artifact, ...] }  # soft result + evidence
acceptance   : { cmd, oracle_paths, timeout_ms, measure? }  # machine-checkable oracle (16 §3.2);
                                                            # runs OUTSIDE the workspace. Absent ⇒
                                                            # zero unattended horizon (16 §6).
hooks        : { pre: [...], post: [...] }    # deterministic entry/exit (git, setup, checks)
```

### 8.4 SandboxProvider behaviour (elaborated in `05`)

```elixir
@behaviour Mitos.SandboxProvider
prepare(launch_spec)        :: {:ok, handle}    # inject workspace+image+RO-data+context; NO secrets in env; wire egress→broker
start(handle, argv, env)    :: {:ok, session}
stream(session)             :: Enumerable.t(event)   # NORMALIZED harness events (§8.6)
control(session, command)   :: :ok              # steer: approve/answer/interrupt/select/prompt
snapshot(session)           :: {:ok, snap} | :unsupported
harvest(session)            :: {:ok, %{diff, result, artifacts}}
stop(session)               :: :ok
capabilities()              :: %{isolation, remote?, egress: :broker, data_mount, snapshot?, net_modes}
```

### 8.5 Authority boundary (the sandbox edges – elaborated in `05`)

- **Inbound – hermetic injection** at `prepare`: workspace (input repo cloned in),
  capability image, RO DATA snapshot, curated context slices. **No secrets enter
  the sandbox.**
- **Outbound – egress credential broker**: the *only* path out. Sandbox egress is
  deny-all-except-broker. The broker attaches or withholds the real credential per
  `egress_policy` + destination, gates approvals, and logs every crossing as an
  Effect event. Secrets live only at the broker.
- **Harvest**: results come from the workspace diff + a `RESULT` artifact +
  registered artifacts – not a callback the agent initiates.
- **MCP** is a DEMOTED, optional tool-delivery affordance for harnesses that speak
  it. It is never the trust boundary.

### 8.6 Harness adapter contract (modern – elaborated in `03`/`05`)

```
launch(argv, env, cwd)
events()      -> normalized stream of: assistant_text | tool_use | tool_result
                 | token_usage | status | final_result
                 # ONE vocabulary, TWO possible sources (14 §1.1): headless stdout,
                 # or a tail of the harness's own session artifacts written to the
                 # mitos-controlled HOME (§8.6 hermeticity). Downstream cannot tell.
steer(cmd)    -> approve | answer | interrupt | select | prompt
                 # a verified transaction on a quiescence edge, never a keystroke (14 §3.3)
snapshot()    -> {:ok, grid} | {:error, :unstable}   # on demand ONLY; never streamed (14 §3.1)
result()      -> structured
hermeticity   : redirect/neutralize native cross-run memory (HOME, config, session
                store, auto-loaded project memory) UNLESS the capability opts into reuse
capabilities  : { structured_output, mcp_tools?, sessions?, effort_levels,
                  observation: {...} }              # the observation matrix, 14 §4
```

**Normative posture – three channels, one clock (`14`).** The harness is
observed on three channels ranked by trust: **C1** the effect surface (fs / exec
/ net crossings – ground truth of what happened), **C2** the harness's own
session artifacts (structured, and available from an *unmodified interactive*
session because `05 §6` already makes mitos choose where they are written), and
**C3** the terminal. **Run semantics MUST come from C1 and C2**; C3 answers only
the bounded question of what a terminal is waiting for.

A prior implementation **streamed** the PTY – parsed a redrawing TUI
continuously – and it did not compose with audit, proof, or metering. The defect
was *streaming*, not the PTY: a display protocol sampled mid-repaint yields torn
frames, split escape sequences, and no way to distinguish new information from a
redraw. `14 §3` re-admits the terminal under three separations – parse
continuously but **materialize only on demand**, read only at a **quiescence
edge**, answer only a **bounded modal-state question** – which makes a screen
read a pure, digested function of the spool prefix, and therefore replayable and
auditable. **Continuous screen parsing stays banned**; so does PTY as a
live-conversation channel.

### 8.7 Task (work-pool unit – elaborated in `06`)

```
Task {
  id          : string
  tenant      : { org, project }            # multi-tenant scoping
  source      : human | trigger:<id> | connector:<name> | run:<run_id> | purchase:<id> | invoke:<parent>
  pool        : string                       # which pool (container + discipline) – `06` §2
  priority    : int                          # within-pool ordering (+ aging); honored by the discipline
  status      : queued | blocked | scheduled | running | done | failed | parked | cancelled
  deps        : [Dep]                         # typed edges (relation/payload/strength/guard) – `06` §4; cross-pool
  pipeline    : pipeline_ref | null           # explicit recipe, or null → dispatch selects
  payload     : { intent, workspace?, inputs, parameters?, constraints, success_criteria }
               # workspace?  : {kind: repo|dataset|none, ref?, refspec?}  – kind declared by the entrypoint
               #               (12 §4); ref/refspec supplied at run time and resolved+cloned by
               #               `prepare` (§7), so the browser/connector never handles git credentials.
               # inputs      : the run's SUBJECT data (what to work on) – typed by entrypoint.inputs.schema.
               # parameters?: per-run KNOBS (how to run), typed with defaults – distinct from inputs (12 §4).
  budget      : { tokens, usd, wall_ms }
  runs        : [run_id]                      # 0..N executions (retry/resume/branch)
  provenance  : { created_by, depth, parent_task? }   # depth bounds agent-emitted + invoked work
  invoke      : { parent_task, return_to } | null     # set on a subtask: where its result is injected (`06` §5)
  external    : { connector, ref } | null     # e.g. GitHub issue # – for bidi sync
  ts          : { created, ready_at, updated }
}
```

The pool is a `fold` over Task-lifecycle Effect events; `task.status` (queue) is distinct
from `run.status` (execution). Pools + disciplines, the dependency graph, invoke/subtasks,
sources/connectors, and the drainer are specified in `06`.

### 8.8 Application manifest (skeleton – elaborated in `12`)

The **application** is the shipping unit: a bundle of pipelines + the capabilities
they reference + this manifest. It *references* the contracts above; it MUST NOT
redefine their fields. No secrets ever appear in it.

```
ApplicationManifest {
  name, version
  capabilities : [ {ref: <capability_ref|path>, as: <alias>} ]   # pinned atoms (§8.3, identity §03 §9)
  pipelines    : [ {id, file} ]                                  # markdown pipelines (04 §8)
  entrypoints  : [ {id, pipeline, title?, inputs:{schema}, parameters?:[{name,type,default?,description?}], workspace: {kind, ref?, refspec?}|kind, defaults?} ]
  io           : { inputs: [...], outputs: [...] }               # DEFAULT bindings (10 §4); tenant overrides at install
  services     : { requires: [...], wants: [...], units?: [...] } # composition root (06 §9–10)
  requirements : { harnesses, connectors, egress, trust_tier, resources }  # install-time transparency (authority stays per-cap)
}
# identity (immutable, replayable – mirrors 03 §9):
#   app_id = H( canonical(manifest) ‖ sorted(capability_id …) ‖ sorted(pipeline_sha256 …) )
```

A user **runs a pipeline** named by an `entrypoint`, never a bare capability; an
entrypoint maps onto a Task (§8.7) at run time (`12 §4`).

### 8.9 Human decision card (skeleton – elaborated in `15`)

The one shape every request for a human takes, carried in the
`approval_requested` Effect payload. It is an *input* to the substrate, never an
authority path: the agent asks no one for anything (§8.5).

```
HumanDecision {
  decision_id, run_id, caused_by: seq, tenant
  kind          : approve | choose | supply_value | amend_brief | rewind | abort
  reversibility : internal | recoverable | external      # DERIVED deterministically (15 §2)
  question      : string                                  # imperative, <= 200 chars
  context       : { attempted, blocked_by, evidence_refs[] }   # FOLDED state; never a transcript
  options       : [ {id, label, consequence, recommended?} ]
  default       : option_id | null    # MUST be null when reversibility = external
  ttl_ms        : int | null          # MUST be null when default is null, and vice versa
  blast_radius  : { paths[], destinations[], spend }
  cost_of_wrong : { wasted_wall_ms, wasted_usd, recoverable }
  value_schema  : json-schema | null  # kind = supply_value
  dedupe_key    : string
}
HumanResolution { decision_id, resolver, choice, value?, note?, resolved_at, via }
```

**The invariant that must never invert:** reversible acts **fail safe** (a
default, on a clock – silence advances the run); externally-visible acts **fail
secure** (no default, park indefinitely – silence never authorizes an
irreversible action). Reversibility that cannot be derived is `external`;
unclassifiable is not permission (`15 §2.1`).

---

## 9. Glossary

- **Capability** – `(data+skill+tools)` OCI image run under an injected harness; the
  packaging unit for reusable behavior; work identity belongs to Tasks.
- **Pipeline** – a composition of capabilities, authored in markdown (loose format).
- **Orchestrator** – the cross-step agent that enforces/adapts a pipeline by emitting Decision events.
- **Loop-manager** – the intra-step deterministic FSM that drives one capability's iterations.
- **Harness** – a coding-agent CLI mitos wraps (Claude Code, Codex, …).
- **Skill / Tools / Data** – the three capability pillars (§5).
- **Decision event / Effect event** – the two log record types (§8.1–8.2).
- **Broker** – the egress credential broker; the outbound authority point (§8.5).
- **Hermetic** – a run carries only what mitos injected; no harness cross-run state.
- **Trust tier (T0/T1/T2)** – isolation/egress posture selector (§6).
- **Target / Preset** – service-composition units in the work pool plane (§06).
- **Service** – a systemd-like runtime unit (trigger + lifecycle); may invoke a pipeline.
- **Task** – a durable unit of intent in the work pool (status, priority, deps, provenance, payload); 0..N Runs execute it. Human backlog, GitHub issues, trigger events, agent follow-ups, invoked subtasks, and paid jobs are all Tasks. (`06`, §8.7)
- **Run** – one Attempt against a Task, possibly executing a pipeline, recorded as events; a Task may have several (retry/resume/branch).
- **Work pool / Pool** – the durable container of Tasks. A *pool* names only the container; **how a Task is picked is a separate, pluggable `discipline`** – a pool is *not* a queue (FIFO is one discipline). (`06` §2)
- **Discipline** – how the drainer selects the next Task from a pool: `fifo | lifo | priority | weighted-fair | deadline | agent-pick | custom`. (`06` §2)
- **Subtask** – a child Task a capability spawns mid-run via the governed `invoke` affordance; its result returns to the parent (which parks). A full sandboxed/metered Task – unlike an opaque harness sub-agent. (`06` §5)
- **Agentic application** – the shipping unit: a bundle of one or more *pipelines* + the *capabilities* they reference + an application manifest (§8.8: entrypoints, I/O bindings, service defaults), packaged as OCI. Packaged entrypoints MAY run a pipeline; direct managed commands and sessions
  use the common Task/Attempt contract without requiring that packaging. (`12`; user-facing framing `11)
- **Entrypoint** – a pipeline an application exposes as runnable, with an input schema; maps onto a Task (§8.7) at run time. (`12` §4)
- **Connector** – a platform-level **ingress** adapter that normalizes an external signal (chat/email/calendar/webhook/event, the dev trackers kanban-md/GitHub/Linear, or the web app) into a Task/trigger; the pool is the source of truth. (`06` §6, generalized in `10` §3)
- **Sink** – a control-plane **egress** adapter that delivers a run's outcome (notify/deliver/reply/web) after harvest, reading the event log + artifacts. NOT the in-sandbox egress broker (`05` §5.3). (`10` §5)
- **I/O plane** – the layer that gives mitos a world-facing input/output: connectors in, sinks out, the web app as one of each. (`10`)

---

## 10. Design principles

1. Keep control *decisions* agentic, control *mechanism and record* deterministic.
2. The event log is the source of truth: state, trace, replay, audit, billing.
3. Tasks preserve work; capability packages and pipelines compose behavior.
4. Enforce authority at the sandbox boundary, not via a harness protocol.
5. Secrets never enter the sandbox – the broker injects them per policy.
6. Strong isolation scales with distrust (T0→T2); untrusted ⇒ remote µVM.
7. Executor-independent; policy MAY select an eligible harness through runtime admission.
8. Hermetic by default; mitos owns memory, not the harness.
9. Determinism where the space is small, agency where it is large.
10. Verify cheaply and tiered; reuse verification as the self-improvement signal.
11. UI, CLI, API, and automation call the same backend operations.
12. **Thin kernel, seam-first.** The substrate owns the log and folds; every
    pluggable concern is a Go interface (`§8.4`, `§8.6`, orchestrator policy).
    No second implementation lives as an inline branch.
13. **Cohesion over accumulation.** A module owns one concern and stays small
    enough to hold in the head. (A prior implementation's 2.7k-line session
    module was a defect, not a stage.)
14. **Errors are values.** Functions return explicit errors; callers add context
    and handle failures. Panic is reserved for unreachable states.

---

## 11. Specification catalog

[The specification index](../README.md) owns the canonical catalog and precedence.
Product requirements live under `spec/product`, system contracts under
`spec/system`, and architecture decisions under `spec/ADRs`. Numeric references
retain their subsystem identities; the former `13` competitive review is now
[ADR 0003](../ADRs/0003_COMPETITIVE_CONTEXT.md), an informational historical record.
