# 06 – Work Pool

Status: Normative subsystem contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

This is the **Work Pool plane** – the *durable-intent* layer that sits between
**TRIGGERS** and **DISPATCH** in the layer stack (`01_ARCHITECTURE` §3). It owns:

1. the **pool(s)** of durable **Tasks** and their pluggable **scheduling disciplines** (§2),
2. the **Task lifecycle** (§3) and the **dependency graph** between Tasks (§4),
3. the dynamic **invoke / result** mechanism – mitos-brokered **subtasks** (§5),
4. the **sources / connectors** that fill the pool and the **drainer** that empties it (§6–7),
5. the **service runtime + composition layer** that produces and drains the pool (§8–11).

It hands a chosen Task to **dispatch** (`04_CONTROL_PLANE`) and never re-specifies the
run itself. It **keeps** chief-wiggum's service runtime (`chief-wiggum
spec/SERVICES_SPEC.md`) and **adds** the composition layer chief-wiggum omitted. Where
this doc and `01` disagree, `01` wins. RFC 2119 keywords are normative. Sections are
**[KEPT]** (from chief-wiggum), **[NEW]**, or **[CHANGED]**.

---

## 1. Three nouns: pipeline · service · pool

The plane turns on keeping three things distinct:

| | **Pipeline** (`04`) | **Service** (§8) | **Pool** (this plane) |
|---|---|---|---|
| Is | the **WORK** – a composition of capabilities | the **TRIGGER + LIFECYCLE** – a runtime unit | the **DURABLE WORK CONTAINER** – Tasks + a discipline |
| Analogue | a program | a systemd unit (`.service`) | a scheduler run-queue *of any discipline* |
| Authored in | markdown (`01` §1) | declarative unit config | substrate-managed; pools are configured, Tasks enqueued |
| Answers | *what to do* | *when to start it, how to keep it alive* | *what work is pending, how it's picked* |
| Lifetime | one run | continuous / recurring | durable; survives restarts (a `fold`, `01` §4) |

A **service produces Tasks**; the **pool holds them**; the **drainer picks one** (per the
pool's discipline) and hands it to **dispatch → a run**. A **run may emit Tasks back**
into the pool (§6.3) or **invoke** a subtask (§5).

> **Rejected: triggers-in-pipeline-markdown (GitHub-Actions style).** An `on: {cron,
> webhook}` block in the pipeline file cannot express a continuous daemon, a health probe,
> a circuit breaker, or backpressure – those are **service** properties, not work
> properties. Triggers live in the service unit; the pipeline markdown stays pure work. A
> pipeline MAY *declare service dependencies* (§10) – a dependency is not a trigger.

```
SOURCES (produce Tasks)                 WORK POOL                          CONSUMER
 human (UI / kanban-md)        ┐
 service trigger (cron/webhook…) ├─► POOLS of TASKS ─pick by discipline─► DRAINER (§7,
 connector (GitHub / Linear)   ┤     + dependency graph (§4)              a platform service):
 a RUN emits a Task (§6.3)  ◄─┐ ┘     [queued|blocked|scheduled|running|  resolve deps · pick per
 a RUN invokes a subtask (§5)│        done|failed|parked|cancelled]       discipline · under budget
 facilitator purchase (`08`) │                                            └─► DISPATCH → RUN (`04`)
                             └──────── run emits / invokes ◄──────────────────────┘
 PROJECTIONS/SINKS: kanban-md view · GitHub labels+comments · UI board
```

---

## 2. Pools & disciplines – *a pool is not a queue* [NEW]

A **queue** names a discipline (FIFO). A **pool** names only the *container*; **how a
Task is selected from it is a separate, pluggable `discipline`.** This is the core
reframe of this plane.

```
Pool {
  id           : string                  # e.g. "interactive" | "batch" | "paid:gold" | "calls"
  scope        : platform | org | project          # §12
  discipline   : fifo | lifo | priority | weighted_fair | deadline_edf | random | agent_pick | <custom>
  concurrency  : int                     # max Tasks draining to runs at once
  reserved     : int                     # guaranteed capacity (e.g. interactive)
  preempts     : [pool_id]               # may preempt lower pools for capacity
  latency_slo  : duration | null         # target; informs deadline_edf / SLA pools
  fairness_key : "tenant" | "user" | null# weighted_fair partitions by this
}
```

**Disciplines** (how `drain()` chooses the next ready Task – pluggable):

| Discipline | Picks | Use |
|---|---|---|
| `fifo` | oldest ready | batch, fairness-by-arrival |
| `lifo` | newest ready | latency-on-fresh, depth-first subtask drains (§5) |
| `priority` | highest `priority` (+ aging to prevent starvation) | mixed importance |
| `weighted_fair` | round-robins across `fairness_key` by weight | multi-tenant fairness (§12) |
| `deadline_edf` | earliest deadline first | `paid:<sla>` pools, latency SLOs |
| `random` | uniformly at random | load tests, tie-breaking |
| `agent_pick` | **a smart selector agent chooses** the next Task | judgement-heavy backlogs – *the only place an LLM may enter selection, opt-in per pool* |
| `<custom>` | a registered selector fn | escape hatch |

Pools are the old "lanes," now carrying their discipline explicitly:

| Pool | Discipline | Posture |
|---|---|---|
| `interactive` | `priority` + `preempts:[batch]` | reserved capacity, low latency, synchronous wait on the run |
| `batch` | `fifo` | throughput; no reserved capacity; backlog, scheduled work, follow-ups |
| `paid:<sla>` | `deadline_edf` | per-tier capacity + latency SLO, metered (`08`) |
| `calls` | `lifo` (or `agent_pick`) | invoked subtasks (§5) – depth-first keeps parents unblocking |

**All work is a Task; there is no bypass path.** "Interactive" is a pool with a
low-latency discipline + reserved capacity, *not* a separate code path – so backpressure,
fairness, audit, and billing cover everything uniformly.

---

## 3. The Task & its lifecycle – `task.status` ≠ `run.status` [NEW]

chief-wiggum fused these (its kanban `[=]` *means* "a worker is running"). We split them:
**queue lifecycle is the Task; execution is the Run; "in progress" is *derived*** ("has an
active run"). Retries / resumes / re-queues / invokes stop being special cases. (Canonical
Task record: `01` §8.7.)

```
created ─► queued ──deps met (§4)──► scheduled ─► running ─► done
              │  └─deps unmet──────► blocked ─┘             │
              │                                             ├─ failed ─► requeue (backoff/policy) or park
              └─► parked: needs-input | awaiting-invoke (§5) ┘
                                       cancelled
```

| Task status | Meaning |
|---|---|
| `queued` | ready (or will be once deps clear); eligible for its pool's discipline |
| `blocked` | a hard dependency (§4) is unmet |
| `scheduled` | drainer claimed it; a run is opening |
| `running` | has an active run (`04`) |
| `parked` | suspended on a human/approval input, or **awaiting an invoked subtask's result (§5)** |
| `done` / `failed` / `cancelled` | terminal (failed MAY requeue per policy) |

CW markers map cleanly: `[ ]`→queued · `[=]`→running · `[P]`→parked · `[x]`→done ·
`[*]`→failed · `[N]`→cancelled. Every transition is an Effect event (`01` §8.2:
`task_enqueued | task_blocked | … | invoke_requested | subtask_returned`); the pool is a
**`fold`** over them – replayable like everything else.

---

## 4. Dependencies – the Task graph [NEW, generalizes chief-wiggum]

Dependencies are **edges between Tasks**. They are **task-bound, not pool-bound**: two
Tasks in *different* pools may depend on each other, and the graph is resolved across the
whole tenant scope, not within one pool. chief-wiggum had three of these (FS `depends_on`,
guarded completion `on_complete/on_failure`, file-conflict mutex via the MIS optimizer);
this generalizes to a small primitive set plus named patterns.

### 4.1 Primitive axes – every static edge is a combination

| Axis | Values |
|---|---|
| **Relation** (when the edge fires) | `finish→start` (FS, default) · `finish→finish` (FF) · `start→start` (SS) · `start→finish` (SF) |
| **Payload** | `control` (ordering only) · `data` (downstream consumes upstream's result/artifact – injected into its Task payload) |
| **Strength** | `hard` (gates readiness – `blocked` until satisfied) · `soft` (priority hint only, never blocks) |
| **Guard** | `unconditional` · `conditional(pred)` (fires only if the upstream **soft result** (`03`) matches `pred`) |

```
Dep { from: task_id, to: task_id, relation: FS|FF|SS|SF, payload: control|data,
      strength: hard|soft, guard: null | predicate_over_result }
```

### 4.2 Named patterns – sugar over the primitives

| Pattern | Shape | = primitives |
|---|---|---|
| **chain** | A→B | FS |
| **fanout** | A→{B₁…Bₙ} | FS to many – *"end→start," one parent fans out* |
| **join / fan-in** | {A₁…Aₙ}→B | FS/FF + a **join rule**: `all` (barrier) · `any` (race, first wins) · `k_of_n` (quorum) |
| **gate / branch** | A→(B if pred) | FS + `conditional` guard (chief-wiggum's `on_result` lifted to the task graph) |
| **pipeline-of-tasks** | A→B→C | FS chain (distinct from a *pipeline of capabilities* inside one run, `04`) |

### 4.3 Constraints – not enabling edges, scheduling restrictions

| Constraint | Meaning | Enforced by |
|---|---|---|
| **conflict / mutex** | A and B MUST NOT run concurrently (resource/file overlap) | drainer (§7) – generalizes chief-wiggum's MIS PR-merge graph + systemd `Conflicts=` |
| **affinity** | A and B SHOULD co-schedule (same worker/batch) – soft | drainer, best-effort |

The graph MUST be acyclic for enabling edges (a cycle is a load-time error, cf.
chief-wiggum `init-scheduler` cycle detection); constraints (`conflict`/`affinity`) are
not part of the DAG. Readiness = *all hard enabling deps satisfied AND no conflicting Task
running*; the discipline (§2) then orders the ready set.

---

## 5. Invoke / result – subtasks (the dynamic dependency) [NEW]

Mid-run, a capability may need *separate* work done and its **result returned**. This is
sub-agent-shaped but **mitos-determined, not harness-determined**. It is a **dynamic**
dependency (created during execution), and the *only* one where the **caller waits for the
callee**.

### 5.1 Three "more work" mechanisms – de-conflated

| Mechanism | Who decides | Scope | New Task? | Returns to caller? |
|---|---|---|---|---|
| in-run **`insert`** | orchestrator (`04`) | *same* run, same workspace, same harness | no | n/a |
| async **follow-up** (§6.3) | capability emits a Task | separate run, fire-and-forget | yes | **no** |
| **`invoke` / result** ← NEW | capability, mid-run, via the authority channel | separate run | yes (a **subtask**) | **yes** (result injected back) |

### 5.2 Why it's distinct from a harness sub-agent – and better

The subtask is a **full Task**: its own capability package (skill+tools+data), its **own
sandbox** (own trust tier / isolation, `05`), **own metering** and **audit trail** (`08`),
own pool/discipline. A harness sub-agent is opaque, shares the harness's sandbox, and is
not independently packaged/metered/sandboxed. So `invoke` is **"sub-agents promoted to
first-class, sandboxed, metered capabilities,"** brokered by mitos – and a Gentix seller's
capability can `invoke` another seller's capability, each sandboxed, metered, and
proof-logged separately.

### 5.3 It does not break the capability packaging boundary

It crosses the boundary the same governed way every outbound effect does (`01` §8.5;
prompt = affordance, env = authority):

```
parent run (capability A)
   │ invoke(capability_ref|task_spec, args)         # governed affordance on the authority channel – typed, policy-checked
   ▼
substrate: create SUBTASK B { parent_task: A, return_to: <A's continuation>, pool: "calls" }
   │  emit Effect invoke_requested (01 §8.2)
   ├─ park A:  task.status = parked:awaiting-invoke (run-state in 04)
   ▼
B runs in its OWN sealed sandbox (05), own metering (08)  ─► B done, harvest B.result
   ▼
substrate: inject B.result into A's context (like any injected slice §8.5) ; emit subtask_returned
   ├─ resume A from its continuation
   ▼
parent A continues with the result
```

Parent and child stay two separate sealed packages; mitos brokers a typed **`invoke`** out
and a typed **result** in. The boundary is intact – the substrate mediates, the parent
never runs the child directly.

### 5.4 Execution: sync by default, async when long [DECIDED]

- **Default – synchronous.** The parent **parks** (`parked:awaiting-invoke`), holding its
  context, and resumes with the result injected. Authors reason about it as a function call.
- **Substrate MAY transparently convert to async continuation** when the subtask is
  long-running or budget-heavy (projected wall-time / budget over a policy threshold): it
  **checkpoints the parent and releases its sandbox**, and represents the rest of the
  parent as a continuation Task with a **`data` dependency (join-on-result, §4)** on the
  subtask. To the capability author it still *looks* like a call returning a result; the
  conversion is a pure substrate optimization (don't hold an idle sandbox open for minutes).

### 5.5 Governance

`invoke` is governed exactly like agent-emitted Tasks, plus call semantics: an
**invoke-depth cap** (recursion limit), **budget drawn from the parent / tenant**, a
**timeout** (subtask timeout → parent resumes with an error result, not a hang), a
**policy allowlist** of invokable capabilities, and **dedup**. A subtask is a Task, so its
own deps (§4) and pool (§2) apply.

---

## 6. Sources & intake [KEPT shape / CHANGED wiring]

Every source resolves to the same durable unit – a **Task** – which is **enqueued** into a
pool, not run directly. **One pool, many provenances.** The trigger/source is the only
fully deterministic, mitos-owned step in *starting* work.

### 6.1 Triggers (service-owned)

| Trigger | How it produces a Task |
|---|---|
| `cron` / `interval` | a service fires on schedule match (`SERVICES_SPEC` §Schedule Types) |
| `continuous` | the unit is a daemon; the Task is its own run, restarted per `restart_delay` |
| `event` | an Effect event (`01` §8.2) matches `trigger` (incl. `service.succeeded:{id}` chaining) |
| `webhook` | inbound HTTP (e.g. a Gentix endpoint, `08` §6) → Task; `idempotency_key` from delivery id |
| `external_signal` | external system / queue message |
| `manual` | operator / API / `mitos run <service>` |

### 6.2 Connectors – the pool is the truth, trackers are adapters

The internal pool is the source of truth; **kanban-md** and **GitHub / Linear issues** are
optional **bidirectional connectors** (issue→Task on import; Task status→labels/comments on
export). No connector is required, and **none implies a git repo** – a code `workspace` is
an *optional* field on the Task payload (`repo | dataset | none`). Same pluggable spirit as
the harness / transport abstractions.

### 6.3 Agent-emitted Tasks – work begets work, governed

Distinct from `insert` (§5.1) and `invoke` (§5): a capability **emits a proposed Task** (an
Effect); the substrate enqueues it under policy, **fire-and-forget** (no return). Runaway
guardrails: a `provenance.depth` cap, **dedup on enqueue** (contradiction-supersession, cf.
`07`), and a per-tenant budget cap on agent-emitted Tasks.

---

## 7. The drainer & dispatch [CHANGED]

Draining the pool is itself a built-in **platform service** (§11) – chief-wiggum's
`task-spawner` was exactly this. Each tick, for each pool, it:

1. computes the **ready set** (§4: hard deps satisfied, no conflicting Task running),
2. **picks** per the pool's `discipline` (§2), within `concurrency` / `reserved` / budget,
   respecting cross-tenant `weighted_fair` (§12),
3. hands the chosen Task to **dispatch** (`01` §3; `04` §3): select pipeline → open a run →
   orchestrator.

Selection is **deterministic** unless the pool's discipline is `agent_pick` – fuzzy
prioritization is opt-in per pool, never a global control layer. Dispatch, run admission,
and everything after belong to `04` (and, for paid endpoints, x402 admission in `08` §5).
**This plane owns the pools, disciplines, Task lifecycle, dependency graph, invoke, and the
drainer; it hands a chosen Task to dispatch.**

---

## 8. The service runtime – KEPT from chief-wiggum [KEPT]

chief-wiggum's `SERVICES_SPEC.md` got the **unit** right: a systemd-like declarative
runtime unit. Mitos keeps it (full field semantics: `SERVICES_SPEC` §"Service Definition"
.. §"Advanced Features" – not re-derived here):

| Concern | Kept mechanism (chief-wiggum §) | Notes for mitos |
|---|---|---|
| **Schedule / trigger** | `tick · interval · cron · event · continuous` | + `webhook · external_signal · manual` (§6.1) |
| **Chaining** | `triggers.{on_complete,on_failure,on_finish}` → `event` | normalized to mitos Effect events (`01` §8.2) |
| **Health checks** | `file · command · heartbeat`, `on_unhealthy: restart\|kill\|log` | verbatim |
| **Circuit breaker** | `closed→open→half-open`, threshold/cooldown | verbatim; per-tenant scoped (§12) |
| **Concurrency** | `max_instances`, `if_running: skip\|queue\|kill`, `queue_max`, `priority` | verbatim – service-level backpressure (distinct from pool discipline §2) |
| **Restart policy** | `retry\|skip\|abort` + exp backoff | verbatim |
| **Dependencies** | `depends_on` (intra-cycle completion) | KEPT, distinct from Task deps (§4) and composition relations (§9) |
| **Conditions** | `file_exists`, `env_*`, `command` gate | verbatim |
| **State** | per-unit `last_run/status/run_count/circuit_state/metrics` | folded onto the event log (`01` §8.2), not a side file |

**[CHANGED] – two adaptations, no field renames:**

1. **`execution.type` narrows to `{pipeline, capability, internal}`.** chief-wiggum's
   `command`/`function`/`agent` collapse: tenant units run a **pipeline** (`04`) or a single
   **capability** (`03`); raw shell/function becomes **`internal`** – first-party substrate
   handlers only. This generalizes the `svc_*`-prefix allowlist into the tenancy boundary (§11).
2. **The bash tick loop is a substrate internal.** chief-wiggum's
   `startup/pre/periodic/post/shutdown` phases were artifacts of a single-process Ralph loop.
   In mitos, scheduling is a BEAM/OTP property (`01` §7); phases survive only as
   *engine-internal* ordering for first-party `internal` units. **Tenants see triggers, never phases.**

---

## 9. The composition layer – targets / presets / conflicts / derived view [NEW]

> **The diagnosis:** chief-wiggum copied systemd's *units* but **not** systemd's
> *composition layer* – `.target`, presets, `Wants=/Requires=/Conflicts=/Before=/After=`.
> Its flat `groups` are *labels for bulk toggle*, not a composition primitive. Result:
> *too many services, manual per-project enable/disable, interference nobody declared.*

Four primitives, layered above the kept runtime, run in the BEAM control plane (`01` §7)
as the **active-set resolver**.

### 9.1 Targets – activate a coherent bundle

```
Target { id, description, scope: platform|org|project,
         requires: [unit|target],   # hard members – activating pulls these in
         wants:    [unit|target],    # soft members – best-effort
         conflicts_with: [target|unit], after: [target] }
```

### 9.2 Presets – default on/off per project type

```
Preset { project_type: "marketplace-seller"|"ci"|"local-dev"|…,
         enable: [unit|target], disable: [unit|target] }   # zero manual toggling for a new project
```
Precedence: **preset < target membership < explicit project override** (kept
`.ralph/services.json` merge semantics).

### 9.3 Conflicts + ordering – interference becomes *declared*

Units and targets gain the systemd relation set: `part_of`, `wants`, `requires`,
`conflicts_with`, `before`/`after`.

| Relation | Meaning | Failure semantics |
|---|---|---|
| `requires A` | A MUST be active when this is | A fails/absent ⇒ this is **not** activated |
| `wants A` | pull A in if possible | A absent ⇒ this still activates |
| `conflicts_with B` | this and B MUST NOT be co-active | hard-vs-soft → hard wins, soft `masked`; hard-vs-hard → **load-time activation error**, both demanders named |
| `before/after B` | ordering only | sequences start; no pull-in |

**Conflict resolution MUST be explicit, never silent.** Interference that used to be
*discovered in production* becomes a *load-time error*.

### 9.4 Derived "why is it on" view

`mitos status` MUST render the **resolved** active set and, per unit, **why** it is on:

```
$ mitos status --project acme/resize
UNIT                STATE    TRIGGER      WHY (pulled in by)
broker-warm         active   continuous   seller-endpoint.target (requires)
webhook-listener    active   webhook      seller-endpoint.target (requires)
usage-meter         active   interval/180 preset:marketplace-seller (enable)
render-pool         active   continuous   pipeline:resize-and-deliver (requires)
local-poll          masked   –            conflicts_with webhook-listener  (soft, masked)
```
`STATE ∈ {active, inactive, masked, failed, circuit-open}`; `WHY` is computed by the
resolver, not stored.

### 9.5 Resolution algorithm

```
active_set(tenant) =
    ⋃ members(T)        for T ∈ active_targets(tenant)      # §9.1 + §9.2 presets
  ∪ ⋃ requires_svc(P)   for P ∈ active_pipelines(tenant)    # §10 pipeline-as-target
  ∪ explicit_enables(tenant)  ⊖ explicit_disables(tenant)   # §11 override
  then: resolve_conflicts(...) ; topo_sort(before, after) ; annotate_why(...)
```
Targets/presets/pipelines/overrides feed **one union**; conflicts and the derived view
apply to that union, not to any single source.

---

## 10. Intent handles: BOTH targets/presets AND pipeline-declared [NEW]

mitos exposes **both** activation intents and unions them:

1. **Platform targets/presets** for the always-on plumbing (dispatch listener, usage
   metering, broker warmers) – operators manage these as named targets with presets.
2. **Pipelines declare the services they need.** A pipeline (`04`) MAY carry a `requires`
   block; **activating the pipeline pulls in its required services** – the pipeline *acts as
   a target*. A dependency declaration, **not** a trigger (§1).

```
# pipeline frontmatter (04) – declares dependencies, NOT triggers
requires:
  services: [render-pool]
  targets:  [seller-endpoint.target]
```
`ACTIVE_SERVICES = (⋃ active targets/presets) ∪ (services required by active pipelines)`;
conflicts (§9.3) and the derived view (§9.4) apply to the union. A unit pulled by a target
AND a pipeline is one unit with two `WHY` rows.

---

## 11. Platform vs tenant split [NEW]

| chief-wiggum service | Reclassified as | Tenant-visible? |
|---|---|---|
| `pr-sync`, `usage-tracker`, `metrics-export`, `log-rotation` | **internal substrate** (`execution.type=internal`) | no |
| `worker-cleanup`, `state-save`, `orphan-detection`, `restore-workers` | **internal substrate** | no |
| `preflight-*`, `init-scheduler`, `lock-cleanup` | **internal substrate** (bring-up/teardown) | no |
| `autofix`, `memory-extract/analyze`, a published seller endpoint | **tenant pipeline / service** | yes |

Tenants author pipelines (`04`) and MAY reference targets (§9.1); they never author raw
`internal` services. The `svc_*` allowlist is promoted to a **structural tenancy boundary**:
`internal` units are first-party only; tenant units are `pipeline`/`capability` only.
Internal usage/metering plumbing feeds the event-log billing ledger (`01` §8.2, `08` §2) –
it is *how* metering happens, not something a tenant schedules. The **drainer** (§7) is an
`internal` platform service.

---

## 12. Multi-tenant scoping [NEW]

Pools, dependency resolution, and active sets are **per-org / per-project**, mirroring
`Org → Users → {…}` (`08` §3, where a *tenant* is the billing + isolation boundary).

- **`scope ∈ {platform, org, project, run}`** on every pool, unit, and target. The
  active-set resolver (§9.5) and the drainer (§7) run **per (org, project)**.
- **No cross-tenant interference.** `conflicts_with`, pool `concurrency`, and Task
  `conflict` (§4.3) are evaluated **within a tenant scope**; `weighted_fair` (§2) shares a
  shared pool's capacity fairly *across* tenants without leaking work between them.
- **Per-tenant breaker / budget blast-radius.** Circuit-breaker state (§8) and budgets
  (`01` §8.1) are keyed by tenant – one tenant's failing unit trips only its own breaker.
  This is the scheduling-side complement to T2 isolation (`05` / `08` §3).
- Tenant `enable/disable` overrides persist per project, never globally.

---

## 13. Concrete example

A Gentix seller (`08`) publishes an image-resize endpoint.

**(a) Target** – always-on plumbing (enabled by the `marketplace-seller` preset):
```json
{ "id": "seller-endpoint.target", "scope": "project",
  "requires": ["webhook-listener", "broker-warm"], "wants": ["usage-meter"],
  "conflicts_with": ["local-dev.target"] }
```
**(b) Service unit** – the webhook trigger, declared conflict + ordering:
```json
{ "id": "webhook-listener", "part_of": ["seller-endpoint.target"], "after": ["broker-warm"],
  "conflicts_with": ["local-poll"],
  "schedule": { "type": "webhook", "path": "/capabilities/resize/runs" },
  "execution": { "type": "pipeline", "pipeline": "resize-and-deliver" },
  "concurrency": { "max_instances": 8, "if_running": "queue", "queue_max": 64 },
  "scope": "project" }
```
**(c) Pipeline** – the WORK (`04`), declaring a required service; a purchase lands a Task in
the `paid:<sla>` pool (§2):
```markdown
---
name: resize-and-deliver
requires: { services: [render-pool], targets: [seller-endpoint.target] }
---
# Resize and deliver
1. capability: image-resize   (03)        # MAY invoke a `watermark` subtask (§5)
2. capability: deliver-proof  (08 §5)
```
**Resolved active set** (`mitos status`, §9.4):
```
UNIT              STATE   TRIGGER     WHY
webhook-listener  active  webhook     seller-endpoint.target (requires)
broker-warm       active  continuous  seller-endpoint.target (requires)  [before webhook-listener]
usage-meter       active  interval    seller-endpoint.target (wants) ∪ preset:marketplace-seller
render-pool       active  continuous  pipeline:resize-and-deliver (requires)
local-poll        masked  –           conflicts_with webhook-listener (soft)
```

---

## 14. What this plane owns vs delegates

| Owns (this plane) | Delegates |
|---|---|
| **pools + disciplines** (§2), **Task lifecycle** (§3), **dependency graph** (§4), **invoke/subtasks** (§5) | dispatch + run + orchestration (`04`); subtask parent-park run-state (`04`) |
| sources / connectors (§6), the **drainer** (§7) | what a capability/pipeline *is* (`03`/`04`) |
| the kept service runtime (§8) + composition layer (§9–10) | full service field semantics (`chief-wiggum SERVICES_SPEC`) |
| platform-vs-tenant classification (§11), per-tenant scoping (§12) | T2 isolation & broker (`05`); take/metering (`08`); event-log/billing fold (`01` §8.2, `08` §2) |

The thesis in one line: **a pool is a container, not a queue; its discipline, its Task
dependency graph (including mitos-brokered invoke/subtasks), and the systemd composition
layer chief-wiggum skipped are all first-class – and every active unit explains itself.**
