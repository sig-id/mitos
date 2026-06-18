# 06 — Scheduling

Status: draft v0.2 · Date: 2026-06-18

This document specifies the **TRIGGERS** and **DISPATCH** band of the layer stack
(`01_ARCHITECTURE` §3) and the runtime that owns it: how work *starts* and how the
many long-lived units that produce work are *composed and managed*. It elaborates
`01` §3 (triggers → dispatch → orchestrator), §7 (scheduling is BEAM control-plane
property), and the glossary entries **Service / Target / Preset**. It hands the
*work itself* to `04_CONTROL_PLANE` (dispatch → orchestrator → pipeline) and never
re-specifies it. Where this doc and `01` disagree, `01` wins.

It **keeps** chief-wiggum's service runtime (`chief-wiggum spec/SERVICES_SPEC.md`)
essentially verbatim — that shape is correct — and **adds the composition layer
chief-wiggum omitted**. RFC 2119 keywords (MUST / SHOULD / MAY) are normative.
Sections are marked **[KEPT]** (from chief-wiggum, summarized), **[NEW]** (the
management layer), or **[CHANGED]** (kept shape, mitos-adapted).

---

## 1. Clean separation up front: pipeline vs service

The single most important distinction in this document:

| | **Pipeline** (`04_CONTROL_PLANE`) | **Service** (this doc) |
|---|---|---|
| Is | the **WORK** — a composition of capabilities | the **TRIGGER + LIFECYCLE** — a runtime unit |
| Analogue | a program | a systemd unit (`.service`) |
| Authored in | markdown, loose format (`01` §1, glossary) | declarative unit config (JSON/TOML) |
| Answers | *what to do, in what order, under whose policy* | *when to start it, how to keep it alive, what it conflicts with* |
| Owns | capabilities, ordering, verify/distill (`04`) | schedule, health, circuit-breaker, concurrency, restart |
| Lifetime | one run (a fold over the event log, `01` §4) | continuous / recurring; spawns runs |

A **service MAY invoke a pipeline** — that is the normal case for tenant work
(`execution.type = pipeline`, §2). The reverse is forbidden: a pipeline does **not**
carry its own trigger.

> **Rejected: triggers-in-pipeline-markdown (GitHub-Actions style).** Putting an
> `on: { cron, push, webhook }` block inside the pipeline file was considered and
> **rejected**. A static workflow file expresses "fire this DAG on event X" but
> *cannot* express a continuous daemon, a health probe with `on_unhealthy:
> restart`, a circuit breaker, or backpressure (`if_running: queue|skip|kill`).
> Those are **runtime-unit** properties, not work properties. Triggers and
> lifecycle therefore live in the service unit; the pipeline markdown stays pure
> work. A pipeline MAY *declare service dependencies* (§5) — a dependency is not a
> trigger.

```
TRIGGER fires ─► WORK ITEM (§7) ─► DISPATCH (04 §3) ─► ORCHESTRATOR ─► PIPELINE run
   ▲                                                                      (04)
   └── owned by a SERVICE unit (schedule | webhook | event | manual | continuous)
```

---

## 2. The service runtime — KEPT from chief-wiggum [KEPT]

chief-wiggum's `SERVICES_SPEC.md` got the **unit** right: a systemd-like,
declarative runtime unit with schedule types, health, circuit breaker, concurrency,
restart policy, dependencies, and completion-event chaining. Mitos keeps it.
Summary (full field semantics: `SERVICES_SPEC` §"Service Definition" .. §"Advanced
Features" — not re-derived here):

| Concern | Kept mechanism (chief-wiggum §) | Notes for mitos |
|---|---|---|
| **Schedule / trigger** | `tick · interval · cron · event · continuous` (§Schedule Types) | + `webhook · external_signal · manual` from `01` §3; §7 |
| **Chaining** | `triggers.{on_complete,on_failure,on_finish}` → `event` (§Service Chaining) | normalized to mitos Effect events (`01` §8.2), not bash events |
| **Health checks** | `file · command · heartbeat`, `on_unhealthy: restart\|kill\|log` (§Health Checks) | verbatim |
| **Circuit breaker** | `closed→open→half-open`, threshold/cooldown (§Circuit Breaker) | verbatim; per-tenant scoped (§8) |
| **Concurrency** | `max_instances`, `if_running: skip\|queue\|kill`, `queue_max`, `priority` (§Concurrency) | verbatim — this *is* backpressure |
| **Restart policy** | `retry\|skip\|abort` + exp backoff (§Restart Policy) | verbatim |
| **Dependencies** | `depends_on` (intra-cycle completion) (§Dependencies) | KEPT, but distinct from composition relations (§4.3) |
| **Conditions** | `file_exists`, `env_*`, `command` gate (§Conditional Execution) | verbatim |
| **State** | per-unit `last_run/status/run_count/circuit_state/metrics` (§State Management) | folded onto the event log (`01` §8.2), not a side file |

**[CHANGED] — two adaptations, no field renames:**

1. **`execution.type` narrows to `{pipeline, capability, internal}`.** chief-wiggum's
   `command`/`function`/`agent` are collapsed: tenant-facing units run a **pipeline**
   (`04`) or a single **capability** (`03`); raw shell/function execution becomes
   **`internal`** — a first-party substrate handler, never tenant-authorable. This
   *generalizes* chief-wiggum's `svc_*`-prefix allowlist (`SERVICES_SPEC` §Security
   Model) into the platform/tenant boundary (§6).
2. **The bash tick loop is a substrate internal.** chief-wiggum's
   `startup/pre/periodic/post/shutdown` phases (§Phases) were artifacts of a
   single-process Ralph loop. In mitos, scheduling is a BEAM/OTP property (`01` §7);
   phases survive only as *engine-internal* ordering for first-party `internal`
   units. **Tenants never see phases — they see triggers.**

---

## 3. The diagnosis: units without a composition layer [NEW]

State it sharply: **chief-wiggum copied systemd's *units* but not systemd's
*composition layer*.** It has the per-unit machinery (schedule, breaker, health) and
nothing above it.

| systemd has | chief-wiggum has | Consequence of the gap |
|---|---|---|
| `.target` (activate a bundle as one unit) | — (flat `groups` for *bulk toggle* only) | no way to say "turn on the marketplace plumbing" |
| presets (default on/off per role) | — (every service ships `enabled` hard-coded) | every new project hand-toggles the same set |
| `Wants=` / `Requires=` (pull-in) | — | activation intent is implicit, not declared |
| `Conflicts=` (mutual exclusion) | — | interference is **discovered at runtime**, not declared |
| `Before=` / `After=` (ordering) | partial: `depends_on` (intra-tick only) | no cross-unit startup order |
| derived "why is this on" view | `service status` (flat list) | you **guess** why a unit is active |

`groups` (`SERVICES_SPEC` §Service Groups) is the tell: it is *labels for bulk
enable/disable*, not a composition primitive — it cannot pull units in, cannot
exclude, cannot order. The result is the management pain the redesign targets: **too
many services, manual per-project enable/disable, and interference nobody declared.**

---

## 4. The management layer [NEW]

Four primitives, layered above the kept runtime. They run in the BEAM control plane
(`01` §7) as the **active-set resolver**.

```
  TARGETS ─┐    ┌──────────── ACTIVE-SET RESOLVER (§4.5) ───────────┐
  PRESETS ─┼───►│ resolve pull-in (wants/requires) · cut conflicts  │──► active units
PIPELINES ─┘    │ · topo-sort (before/after) · annotate WHY         │        │
(declare reqs)  └──────────────────────────────────────────────────┘        ▼
                                              the SERVICE RUNTIME (§2) runs them
```

### 4.1 Targets / profiles — activate a coherent bundle

A **target** (systemd `.target`) names a bundle activated/deactivated as one unit.
"Bring up the seller-endpoint plumbing" becomes one verb.

```
Target {
  id            : string                 # e.g. "seller-endpoint.target"
  description   : string
  scope         : platform | org | project          # §8
  requires      : [unit_id | target_id]  # hard members — activating the target pulls these in
  wants         : [unit_id | target_id]  # soft members — pulled in best-effort
  conflicts_with: [target_id | unit_id]  # cannot be co-active
  after         : [target_id]            # ordering vs other targets
}
```

### 4.2 Presets — default on/off per project type

A **preset** (systemd preset) fixes the *default* active set for a class of project,
so a new project gets a sane configuration with **zero manual toggling**.

```
Preset {
  project_type : "marketplace-seller" | "ci" | "local-dev" | ...
  enable       : [unit_id | target_id]   # default-on for this project type
  disable      : [unit_id | target_id]   # default-off (overrides a unit's own `enabled`)
}
```

Presets set defaults only; an org/project MAY override (§6 overrides keep
chief-wiggum's `.ralph/services.json` merge semantics, `SERVICES_SPEC` §Project
Overrides). Precedence: **preset < target membership < explicit project override**.

### 4.3 Conflicts + ordering relations — interference becomes *declared*

Units (and targets) gain the systemd relation set. This is the core fix for "nobody
declared the interference."

```
ServiceUnit {                            # = §2 KEPT fields, plus:
  part_of        : [target_id]           # membership (systemd PartOf/WantedBy)
  wants          : [unit_id]             # soft pull-in (systemd Wants=)
  requires       : [unit_id]             # hard pull-in (systemd Requires=)
  conflicts_with : [unit_id]             # mutual exclusion (systemd Conflicts=)
  before / after : [unit_id]             # startup ordering (systemd Before=/After=)
}
```

| Relation | Meaning | Failure semantics |
|---|---|---|
| `requires A` | A MUST be active when this is | A fails/absent ⇒ this is **not** activated |
| `wants A` | pull A in if possible | A absent ⇒ this still activates (best-effort) |
| `conflicts_with B` | this and B MUST NOT be co-active | see resolution below |
| `before/after B` | ordering only (no pull-in) | sequences start; does not imply membership |

**Conflict resolution MUST be explicit, never silent.** When the union (§4.5)
demands two `conflicts_with` units:

- if one is pulled by `requires` (hard) and the other by `wants` (soft) → **hard
  wins**, the soft unit is `masked` (shown, not run);
- if both are hard → **activation error**, surfaced in `mitos status` with both
  demanders named. The manager refuses to guess. Interference that used to be
  *discovered in production* is now a *load-time error*.

### 4.4 Derived authoritative active-set view — never guess

`mitos status` MUST render the **resolved** active set and, for every unit, **why**
it is on (which target / preset / pipeline pulled it in). This is the management
payoff: the active set is a *function of declared intent*, and the function is
inspectable.

```
$ mitos status --project acme/resize
UNIT                STATE    TRIGGER      WHY (pulled in by)
broker-warm         active   continuous   seller-endpoint.target (requires)
webhook-listener    active   webhook      seller-endpoint.target (requires)
usage-meter         active   interval/180 preset:marketplace-seller (enable)
render-pool         active   continuous   pipeline:resize-and-deliver (requires)
local-poll          masked   —            conflicts_with webhook-listener  (soft, masked)
nightly-audit       active   cron 0 3 * * * project override (enable)
```

`STATE ∈ {active, inactive, masked, failed, circuit-open}`; `circuit-open` reuses the
kept breaker state (§2). The `WHY` column is computed by the resolver, not stored.

### 4.5 Resolution algorithm

```
active_set(tenant) =
    ⋃ members(T)        for T ∈ active_targets(tenant)        # §4.1 + §4.2 presets
  ∪ ⋃ requires_svc(P)   for P ∈ active_pipelines(tenant)      # §5 pipeline-as-target
  ∪ explicit_enables(tenant)                                  # §6 project override
  ⊖ explicit_disables(tenant)
  then:  resolve_conflicts(...)        # §4.3 — hard-wins / mask / error
         topo_sort(before, after)      # §4.3 — cycles are a load error (cf. init-scheduler)
         annotate_why(...)             # §4.4
```

Targets/presets/pipelines/overrides all feed **one union**; conflicts and the
derived view apply to that union — not to any single source.

---

## 5. Intent handles: BOTH targets/presets AND pipeline-declared services [NEW]

Decision: mitos exposes **both** activation intents, and unions them.

1. **(a) Platform targets/presets** for the **always-on plumbing** — the dispatch
   listener, usage metering, broker warmers. Operators manage these as named
   targets (§4.1) with presets per project type (§4.2).
2. **(b) Pipelines declare the services they need.** A pipeline (`04`) MAY carry a
   `requires` block; **activating the pipeline pulls in its required services** — the
   pipeline *acts as a target*. This is a dependency declaration, **not** a trigger
   (§1), so the §1 rejection still holds.

```
# pipeline frontmatter (04) — declares dependencies, NOT triggers
requires:
  services: [render-pool]          # pulled into active_set while this pipeline is active
  targets:  [seller-endpoint.target]
```

The authoritative active service set is the **union**:

```
ACTIVE_SERVICES = ( ⋃ active targets/presets )  ∪  ( services required by active pipelines )
```

Conflicts (§4.3) and the derived view (§4.4) apply to the union. A unit pulled by a
target AND a pipeline is one unit with two `WHY` rows — not two instances.

---

## 6. Platform vs tenant split [NEW]

chief-wiggum's default `services.json` mixes two populations. The redesign splits
them:

| chief-wiggum service | Reclassified as | Visible to tenant? |
|---|---|---|
| `pr-sync`, `usage-tracker`, `metrics-export`, `log-rotation` | **internal substrate** (`execution.type=internal`) | no — engine plumbing |
| `worker-cleanup`, `state-save`, `orphan-detection`, `restore-workers` | **internal substrate** | no |
| `preflight-*`, `init-scheduler`, `lock-cleanup` | **internal substrate** (engine bring-up/teardown) | no |
| `autofix`, `memory-extract/analyze`, a published seller endpoint | **tenant pipeline / service** | yes |

- **Tenants author pipelines (`04`) and MAY reference targets (§4.1); they never
  author raw `internal` services.** The substrate's own plumbing is invisible to
  them — it is the engine keeping itself alive, metering, and cleaning up.
- This is the `svc_*` allowlist (`SERVICES_SPEC` §Security Model) promoted to a
  **tenancy boundary**: `internal` units are first-party only; tenant units are
  `pipeline`/`capability` only. The boundary is structural, not naming-convention.
- Internal usage/metering plumbing feeds the event-log billing ledger (`01` §8.2,
  `08` §2) — it is *how* metering happens, not something a tenant schedules.

---

## 7. Triggers → work item → dispatch [KEPT shape / CHANGED wiring]

Every trigger type resolves to the same thing: it **produces a work item** and hands
it to **dispatch** (`01` §3, `04`). The trigger is the only mitos-owned, fully
deterministic decision in starting a run.

```
WorkItem {
  id             : string
  tenant         : { org, project }          # §8 — every work item is tenant-scoped
  source         : cron | interval | continuous | event | webhook | external_signal | manual
  service_id     : string                    # the unit that fired
  target         : pipeline_ref | capability_ref   # what to run (04 / 02)
  params         : object                    # trigger payload (webhook body, cron ctx, …)
  idempotency_key: string                    # de-dupe redelivered webhooks/events
  ts             : iso8601
}
        │
        ▼   DISPATCH (04 §3): cheap classifier, no LLM
   route WorkItem → open a run → ORCHESTRATOR → pipeline (04)
```

| Trigger | Source of the work item |
|---|---|
| `cron` / `interval` | scheduler fires on schedule match (`SERVICES_SPEC` §Schedule Types) |
| `continuous` | unit is a daemon; "work item" is its own run, restarted per `restart_delay` |
| `event` | an Effect event (`01` §8.2) matches `trigger` (incl. `service.succeeded:{id}` chaining) |
| `webhook` | inbound HTTP (e.g. a Gentix endpoint, `08` §6) → work item; `idempotency_key` from delivery id |
| `external_signal` | external system / queue message |
| `manual` | operator / API / `mitos run <service>` |

Dispatch, run admission, and everything after the work item belong to `04` (and, for
paid endpoints, x402 admission in `08` §5). This doc stops at the work item.

---

## 8. Multi-tenant scoping [NEW]

Active sets are **per-org / per-project**, mirroring the tenancy model `Org → Users →
{...}` (`08` §3, where a *tenant* is the billing + isolation boundary).

- **`scope ∈ {platform, org, project, run}`** on every unit and target. Resolution
  (§4.5) runs **once per (org, project)**; a project's active set is computed only
  from intents in scope for that project.
- **No cross-tenant interference.** `conflicts_with` and concurrency limits are
  evaluated **within a tenant scope** — one tenant's `webhook-listener` never masks,
  blocks, or shares a concurrency queue with another's. A `platform`-scope unit is
  shared infra; it MUST be `internal` (§6) and MUST NOT carry tenant data.
- **Per-tenant breaker / budget blast-radius.** Circuit-breaker state (§2) and
  budgets (`01` §8.1) are keyed by tenant, so one tenant's failing unit trips only
  its own breaker. This is the scheduling-side complement to the T2 isolation in
  `05`/`08` §3.
- Tenant `enable/disable` overrides (§4.2) persist per project (kept
  `.ralph/services.json` → mitos per-project unit store), never globally.

---

## 9. Concrete example

A Gentix seller (`08`) publishes an image-resize endpoint. Three artifacts, one
coherent active set.

**(a) Target** — the always-on plumbing for any published endpoint (platform-managed,
enabled by the `marketplace-seller` preset):

```json
{
  "id": "seller-endpoint.target",
  "description": "Always-on plumbing for a published Gentix endpoint",
  "scope": "project",
  "requires": ["webhook-listener", "broker-warm"],
  "wants":    ["usage-meter"],
  "conflicts_with": ["local-dev.target"]
}
```

**(b) Service unit** — the webhook trigger, with a declared conflict and ordering:

```json
{
  "id": "webhook-listener",
  "description": "Inbound x402 run requests for /capabilities/{id}/runs",
  "part_of": ["seller-endpoint.target"],
  "after":   ["broker-warm"],
  "conflicts_with": ["local-poll"],
  "schedule": { "type": "webhook", "path": "/capabilities/resize/runs" },
  "execution": { "type": "pipeline", "pipeline": "resize-and-deliver" },
  "concurrency": { "max_instances": 8, "if_running": "queue", "queue_max": 64 },
  "circuit_breaker": { "enabled": true, "threshold": 5, "cooldown": 300 },
  "scope": "project"
}
```

**(c) Pipeline** — the WORK (`04`), declaring a *required service* (not a trigger):

```markdown
---
name: resize-and-deliver
requires:
  services: [render-pool]            # pulled into the active set; pipeline-as-target (§5)
  targets:  [seller-endpoint.target]
---
# Resize and deliver
1. capability: image-resize   (03)
2. capability: deliver-proof  (08 §7)
```

**Resolved active set** (`mitos status`, §4.4):

```
UNIT              STATE   TRIGGER     WHY
webhook-listener  active  webhook     seller-endpoint.target (requires)
broker-warm       active  continuous  seller-endpoint.target (requires)  [before webhook-listener]
usage-meter       active  interval    seller-endpoint.target (wants) ∪ preset:marketplace-seller
render-pool       active  continuous  pipeline:resize-and-deliver (requires)
local-poll        masked  —           conflicts_with webhook-listener (soft)
```

One published endpoint, one declared bundle, one union, zero manual per-service
toggling — and every active unit explains itself.

---

## 10. What this doc owns vs delegates

| Owns (this doc) | Delegates |
|---|---|
| triggers, schedule/trigger types, work-item shape (§7) | dispatch + run + orchestration (`04`) |
| the kept service runtime summary (§2) | full field semantics (`chief-wiggum SERVICES_SPEC`) |
| targets / presets / conflicts / ordering / derived view (§3–4) | what a capability/pipeline *is* (`03`/`04`) |
| platform-vs-tenant unit classification (§6) | T2 isolation & broker (`05`); take/metering (`08`) |
| per-tenant active-set scoping (§8) | event-log/billing fold (`01` §8.2, `08` §2) |

The thesis in one line: **keep chief-wiggum's correct unit; add the systemd
composition layer it skipped; make the active set a declared, inspectable union over
targets, presets, and pipeline-declared dependencies.**
