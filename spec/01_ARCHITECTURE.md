# 01 — Architecture (Constitution)

Status: draft v0.2 · Date: 2026-06-18 · Supersedes the entire prior `spec/` set
(old `00_ARCHITECTURE`, `01_PRODUCT`, `02_HLD`, `03_NUCLEUS_INTEGRATION`,
`05_LAYERING`, `06_AUTHORITY_CALLBACK_PLANE`, `07_GENTIX_FACILITATOR`).

This document is the constitution. It fixes the vocabulary, the layer model, the
control model, and the **canonical contracts** (§8) that every other spec in this
directory MUST conform to. Where another doc and this one disagree, this one wins.

RFC 2119 keywords (MUST / SHOULD / MAY) are normative.

---

## 0. What this redesign changed

Mitos was previously specced as a *deterministic control plane executing an
explicit, pre-declared run state machine* over a Zig transport engine. This
redesign — informed by a study of chief-wiggum (a Ralph-Wiggum orchestrator) —
keeps the deterministic-substrate thesis but moves the *control decisions* into
an agent, and relocates determinism into **recording and limits** rather than
**pre-authored transitions**. It also makes mitos **cloud-first and
multi-tenant**, and elevates a packaged **capability** to the atomic unit of both
execution and monetization.

The one-line thesis:

> **Mitos is a cloud-first, multi-tenant substrate that runs packaged
> capabilities through agent-driven pipelines, where an agent decides policy and
> deterministic code records every decision and enforces every limit.**

---

## 1. The constitutional claims

1. **Capability is the atom.** The unit of execution is a *capability* =
   `(data + skill + tools)` packaged as an OCI image, run under an LLM through a
   pluggable harness, inside a sandbox. A *pipeline* is a composition of
   capabilities. The same capability is the unit a Gentix seller monetizes
   (`08_FACILITATOR`).
2. **Agentic policy, deterministic record.** The orchestrator agent decides *what
   happens next* in free-form reasoning, but it can only act by emitting a typed
   **Decision event** that the deterministic substrate executes through controlled
   mechanisms and appends to an immutable log. State is **emergent and
   replayable**, never pre-declared. There is no authored finite-state machine and
   no formal-methods (TLA) obligation.
3. **The sandbox boundary is the security boundary.** Authority is enforced at the
   edges of the sandbox — hermetic *injection* inbound, an *egress credential
   broker* outbound, *harvest* of results — not by trusting the agent and not by a
   protocol the harness must speak. (See §8.5.)
4. **Harness-agnostic.** Mitos wraps coding-agent harnesses (Claude Code, Codex,
   Gemini CLI, OpenCode, …) and never hard-codes one. The harness does the hard
   work; mitos owns everything outside it.
5. **Env is authority, prompt is affordance.** What an agent *may* do is granted
   by injected environment + the broker; what it is *told* it can do is prompt.
6. **Cloud-first, locally runnable.** The primary target is a hosted multi-tenant
   service; running the whole thing on one developer machine is a supported
   secondary mode.

---

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

```
TRIGGERS / SOURCES (det.)  cron · webhook · event · manual · connector(GitHub/kanban) · run-emitted   [§06]
  └─► WORK POOL (det.)     durable Tasks in pools, each with a discipline (fifo|priority|deadline|agent-pick…)
  │                        + a dependency graph (incl. invoke/subtasks); a platform service drains it   [§06, §8.7]
  └─► DISPATCH (det.)  select pipeline  (cheap classifier, no LLM)
       └─► ORCHESTRATOR AGENT ── Decision events ──► EVENT LOG (det., canonical, append-only)
       │   reads live run state; advance / insert / retry / switch / skip /
       │   request_approval / escalate / finish / abort.  The pipeline is ENFORCED here,
       │   not pre-declared. (§04)
       └─► STEP RUNTIME (per capability)   entry/exit contract + deterministic hooks
            └─► LOOP-MANAGER (det. FSM; model only on escalation)
            │   continue · compact-context · switch model/effort/harness · escalate · done  (§04)
            └─► HARNESS (injected, agnostic)   Claude Code | Codex | Gemini | OpenCode
                 └─► CAPABILITY in a sandbox (§05):
                      image[SKILL+TOOLS] + DATA[RO snapshot] + workspace[input]
                      ① inject (hermetic)   ② observe/steer (mitos-driven)   ③ egress→BROKER
SUBSTRATE (det., BEAM): event log/trace · memory (§07) · egress broker + policy (§05) ·
                        work pool + scheduling (§06) · budgets/metering (§08) · multi-tenancy/keys (§08)
```

Determinism where the decision space is small (the loop-manager is a coded FSM);
agency where it is large (the orchestrator reasons over an open pipeline).

---

## 4. Control model — agentic policy, deterministic record

The orchestrator never mutates state directly. Each step it emits a **Decision
event** (§8.1); the substrate executes the chosen action through deterministic
mechanisms and emits resulting **Effect events** (§8.2). The append-only,
ordered union of Decision + Effect events **is** the run state and the source of
truth.

```
state(t) = fold(events[0..t])
resume   = replay events to last committed Effect, then re-invoke the orchestrator
```

**Crash test (must pass):** if the process dies after a Decision but before the
Effect commits, mitos recovers without guessing — the Decision is on the log, the
Effect is not, so the action is simply re-driven. Reasoning is stochastic; the
record of *what was decided and done* is total.

Two agentic control layers, no third:

- **Orchestrator** (cross-step): adapts the pipeline, handles stuck/edge cases,
  routes (dispatch is folded in). One agent.
- **Loop-manager** (intra-step): a deterministic FSM that composes the next
  iteration's context and only spends a model call to decide an escalation or a
  model/effort/harness switch. (Replaces chief-wiggum's dumb summarize-and-loop.)

---

## 5. The capability (atom) — see `03_CAPABILITY`

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

| BEAM/Elixir (control plane — owns all orchestration) | Zig (engine — local only) | Sandbox (per run) |
|---|---|---|
| orchestrator agent host, loop-manager FSM, event log/trace writer, policy compiler, **egress credential broker**, **remote** provider clients (E2B/Daytona over HTTP/WS), scheduling/services, memory, multi-tenancy/orgs/keys/metering, web/WS UI | **local** transports (pty/stdio), terminal codec (libghostty-vt), raw spool + first-pass redaction, local sandbox launch (Nucleus/Docker), Tier-2 synchronous enforcement | harness process + capability image + RO data + workspace + (optional MCP tool relay) |

Languages: Elixir/BEAM + Zig only. No Go, no Rust, no TypeScript.

---

## 8. Canonical contracts (source of truth)

Other specs elaborate these; they MUST NOT redefine field names or signatures.

### 8.1 Decision event (orchestrator / loop-manager → substrate)

```
DecisionEvent {
  run_id      : string
  seq         : u64                 # monotonic per run
  ts          : iso8601
  actor       : "orchestrator" | "loop_manager"
  type        : advance | insert | retry | switch | skip
              | request_approval | escalate | finish | abort
  target      : capability_ref | stage_id | null
  params      : object              # type-specific (e.g. switch → {model,effort,harness})
  inputs_hash : string             # hash of the state the decision was taken on (replay integrity)
  rationale   : string             # NL justification — AUDIT ONLY, never controls flow
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
            | task_enqueued | task_blocked | task_done | invoke_requested | subtask_returned | ...
  caused_by : seq                   # the DecisionEvent that led here (causality)
  ref       : object                # ids / paths
  payload   : object
}
```

The **event log** is the append-only ordered union of Decision + Effect events.
It is simultaneously: run state, debug trace, replay source, audit record, and
billing ledger (`08`).

### 8.3 Capability manifest (skeleton — elaborated in `03`)

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

### 8.5 Authority boundary (the sandbox edges — elaborated in `05`)

- **Inbound — hermetic injection** at `prepare`: workspace (input repo cloned in),
  capability image, RO DATA snapshot, curated context slices. **No secrets enter
  the sandbox.**
- **Outbound — egress credential broker**: the *only* path out. Sandbox egress is
  deny-all-except-broker. The broker attaches or withholds the real credential per
  `egress_policy` + destination, gates approvals, and logs every crossing as an
  Effect event. Secrets live only at the broker.
- **Harvest**: results come from the workspace diff + a `RESULT` artifact +
  registered artifacts — not a callback the agent initiates.
- **MCP** is a DEMOTED, optional tool-delivery affordance for harnesses that speak
  it. It is never the trust boundary.

### 8.6 Harness adapter contract (modern — elaborated in `03`/`05`)

```
launch(argv, env, cwd)
events()      -> normalized stream of: assistant_text | tool_use | tool_result
                 | token_usage | status | final_result
steer(cmd)
result()      -> structured
hermeticity   : redirect/neutralize native cross-run memory (HOME, config, session
                store, auto-loaded project memory) UNLESS the capability opts into reuse
capabilities  : { structured_output, mcp_tools?, sessions?, effort_levels, ... }
```

This supersedes the old PTY/`claude_log` screen-scraping path: structured
observation is re-specced per harness generation; PTY is a fallback codec.

### 8.7 Task (work-pool unit — elaborated in `06`)

```
Task {
  id          : string
  tenant      : { org, project }            # multi-tenant scoping
  source      : human | trigger:<id> | connector:<name> | run:<run_id> | purchase:<id> | invoke:<parent>
  pool        : string                       # which pool (container + discipline) — `06` §2
  priority    : int                          # within-pool ordering (+ aging); honored by the discipline
  status      : queued | blocked | scheduled | running | done | failed | parked | cancelled
  deps        : [Dep]                         # typed edges (relation/payload/strength/guard) — `06` §4; cross-pool
  pipeline    : pipeline_ref | null           # explicit recipe, or null → dispatch selects
  payload     : { intent, workspace?, inputs, constraints, success_criteria }  # workspace optional: repo|dataset|none
  budget      : { tokens, usd, wall_ms }
  runs        : [run_id]                      # 0..N executions (retry/resume/branch)
  provenance  : { created_by, depth, parent_task? }   # depth bounds agent-emitted + invoked work
  invoke      : { parent_task, return_to } | null     # set on a subtask: where its result is injected (`06` §5)
  external    : { connector, ref } | null     # e.g. GitHub issue # — for bidi sync
  ts          : { created, ready_at, updated }
}
```

The pool is a `fold` over Task-lifecycle Effect events; `task.status` (queue) is distinct
from `run.status` (execution). Pools + disciplines, the dependency graph, invoke/subtasks,
sources/connectors, and the drainer are specified in `06`.

### 8.8 Application manifest (skeleton — elaborated in `12`)

The **application** is the shipping unit: a bundle of pipelines + the capabilities
they reference + this manifest. It *references* the contracts above; it MUST NOT
redefine their fields. No secrets ever appear in it.

```
ApplicationManifest {
  name, version
  capabilities : [ {ref: <capability_ref|path>, as: <alias>} ]   # pinned atoms (§8.3, identity §03 §9)
  pipelines    : [ {id, file} ]                                  # markdown pipelines (04 §8)
  entrypoints  : [ {id, pipeline, title?, inputs:{schema}, workspace: repo|dataset|none, defaults?} ]
  io           : { inputs: [...], outputs: [...] }               # DEFAULT bindings (10 §4); tenant overrides at install
  services     : { requires: [...], wants: [...], units?: [...] } # composition root (06 §9–10)
  requirements : { harnesses, connectors, egress, trust_tier, resources }  # install-time transparency (authority stays per-cap)
}
# identity (immutable, replayable — mirrors 03 §9):
#   app_id = H( canonical(manifest) ‖ sorted(capability_id …) ‖ sorted(pipeline_sha256 …) )
```

A user **runs a pipeline** named by an `entrypoint`, never a bare capability; an
entrypoint maps onto a Task (§8.7) at run time (`12 §4`).

---

## 9. Glossary

- **Capability** — `(data+skill+tools)` OCI image run under an injected harness; the
  atomic unit of execution and monetization.
- **Pipeline** — a composition of capabilities, authored in markdown (loose format).
- **Orchestrator** — the cross-step agent that enforces/adapts a pipeline by emitting Decision events.
- **Loop-manager** — the intra-step deterministic FSM that drives one capability's iterations.
- **Harness** — a coding-agent CLI mitos wraps (Claude Code, Codex, …).
- **Skill / Tools / Data** — the three capability pillars (§5).
- **Decision event / Effect event** — the two log record types (§8.1–8.2).
- **Broker** — the egress credential broker; the outbound authority point (§8.5).
- **Hermetic** — a run carries only what mitos injected; no harness cross-run state.
- **Trust tier (T0/T1/T2)** — isolation/egress posture selector (§6).
- **Target / Preset** — service-composition units in the work pool plane (§06).
- **Service** — a systemd-like runtime unit (trigger + lifecycle); may invoke a pipeline.
- **Task** — a durable unit of intent in the work pool (status, priority, deps, provenance, payload); 0..N Runs execute it. Human backlog, GitHub issues, trigger events, agent follow-ups, invoked subtasks, and paid jobs are all Tasks. (`06`, §8.7)
- **Run** — one execution of a pipeline against a Task, recorded as events; a Task may have several (retry/resume/branch).
- **Work pool / Pool** — the durable container of Tasks. A *pool* names only the container; **how a Task is picked is a separate, pluggable `discipline`** — a pool is *not* a queue (FIFO is one discipline). (`06` §2)
- **Discipline** — how the drainer selects the next Task from a pool: `fifo | lifo | priority | weighted-fair | deadline | agent-pick | custom`. (`06` §2)
- **Subtask** — a child Task a capability spawns mid-run via the governed `invoke` affordance; its result returns to the parent (which parks). A full sandboxed/metered Task — unlike an opaque harness sub-agent. (`06` §5)
- **Agentic application** — the shipping unit: a bundle of one or more *pipelines* + the *capabilities* they reference + an application manifest (§8.8: entrypoints, I/O bindings, service defaults), packaged as OCI. A user **runs a pipeline**, never a bare capability. (`12`; user-facing framing `11` §2)
- **Entrypoint** — a pipeline an application exposes as runnable, with an input schema; maps onto a Task (§8.7) at run time. (`12` §4)
- **Connector** — a platform-level **ingress** adapter that normalizes an external signal (chat/email/calendar/webhook/event, the dev trackers kanban-md/GitHub/Linear, or the web app) into a Task/trigger; the pool is the source of truth. (`06` §6, generalized in `10` §3)
- **Sink** — a control-plane **egress** adapter that delivers a run's outcome (notify/deliver/reply/web) after harvest, reading the event log + artifacts. NOT the in-sandbox egress broker (`05` §5.3). (`10` §5)
- **I/O plane** — the layer that gives mitos a world-facing input/output: connectors in, sinks out, the web app as one of each. (`10`)

---

## 10. Design principles

1. Keep control *decisions* agentic, control *mechanism and record* deterministic.
2. The event log is the source of truth: state, trace, replay, audit, billing.
3. Capability is the atom; pipeline is composition; both are markdown-authored.
4. Enforce authority at the sandbox boundary, not via a harness protocol.
5. Secrets never enter the sandbox — the broker injects them per policy.
6. Strong isolation scales with distrust (T0→T2); untrusted ⇒ remote µVM.
7. Harness-agnostic; the orchestrator never names a harness.
8. Hermetic by default; mitos owns memory, not the harness.
9. Determinism where the space is small, agency where it is large.
10. Verify cheaply and tiered; reuse verification as the self-improvement signal.
11. UI, CLI, API, and automation call the same backend operations.

---

## 11. Spec map

| Doc | Scope |
|---|---|
| `01_ARCHITECTURE` (this) | Constitution: vocabulary, layers, control model, canonical contracts |
| `02_PRODUCT` | Thesis, users, multi-tenant framing, what mitos is/isn't, MVP scope |
| `03_CAPABILITY` | The capability: pillars, per-model skill seam, manifest, hooks, result+evidence, authoring |
| `04_CONTROL_PLANE` | Orchestrator + loop-manager, Decision/Effect schema, resume/replay, pipeline-in-markdown, verify+distill |
| `05_COMPUTE_PLANE` | SandboxProvider, trust tiers, providers, authority boundary (inject/broker/harvest), hermeticity |
| `06_WORK_POOL` | **Work pool plane**: Tasks, pools + disciplines, dependency graph, invoke/subtasks + service runtime (kept) + composition (targets/presets/conflicts) + triggers |
| `07_MEMORY` | Two-track memory, retrieval/injection, contradiction-supersession forgetting |
| `08_FACILITATOR` | Gentix: capability=monetizable unit, metering off the log, multi-tenancy, take-rate |
| `09_IMPLEMENTATION_PLAN` | Phased build on BEAM+Zig, reuse map, sequencing |
| `10_IO_PLANE` | **I/O plane**: connectors (ingress: chat/email/calendar/webhook/event/web), sinks (egress: notify/deliver/reply/web), application I/O bindings; distinct from the in-sandbox broker (`05`) |
| `11_FRONTEND` | **Frontend product spec**: app⊃pipeline⊃capability hierarchy, three surfaces (Run & Watch two-zoom · Build & Test · Fleet), CLI/web primacy, packaging lifecycle |
| `12_APPLICATION` | **Application packaging & manifest** (`§8.8`): capabilities/pipelines/entrypoints, input→Task mapping, ship-vs-bind I/O defaults, composition root, `app_id` identity, OCI bundling |
