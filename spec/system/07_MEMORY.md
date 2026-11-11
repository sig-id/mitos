# 07 – Memory

Status: Normative subsystem contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

Conforms to `01_ARCHITECTURE` (the constitution). This doc owns the **cross-run
knowledge service**: OS-owned, multi-tenant, two-track memory with write-time
contradiction-supersession. It is the substrate hook named in `01 §3` (SUBSTRATE:
"memory (§07)") and in the Decision/Effect contract via the `memory_written`
Effect event (`01 §8.2`).

RFC 2119 keywords are normative.

---

## 1. The split (decided): DATA is not memory

Two things are easy to conflate. They are different and live in different specs.

| | **Per-capability DATA** (the DATA pillar) | **Cross-run KNOWLEDGE** (this doc) |
|---|---|---|
| What | the capability's own corpus / refs / DB | distilled lessons + metrics *across runs* |
| Owner | the capability author, baked into the package | **the OS (mitos)** |
| Mutability | read-only at run time | append + supersede, OS-validated |
| Scope | the capability | tenant × capability × model |
| Lifecycle | versioned with the capability image | grows/forgets per run |
| Delivery | RO provider snapshot mounted at `/data` | injected context slice (§5) / broker fetch |
| Spec | `01 §5` (DATA), manifest in `03`, mount/snapshot in `05` | **`07` (here)** |

**DATA is NOT the memory service.** A capability's reference corpus is packaged,
content-addressed, and mounted RO via `prepare` (`05 §SandboxProvider`,
`01 §8.4`). It does not learn, is not tenant-scoped, and emits no
`memory_written`. Everything below concerns the second column only – the
*knowledge* a tenant accumulates by *running* capabilities. Detail on the DATA
mount belongs to `03`/`05`; we delegate it and never duplicate it.

---

## 2. Two tracks (we keep chief-wiggum's shape, promote it to an OS service)

chief-wiggum's `lib/memory/memory.sh` already split memory cleanly into a
**deterministic** half (jq-computed stats in `.json`) and a **qualitative** half
(LLM-written `.md` by `memory-analyst` / consumed by `self-improver`). We keep
that two-track shape verbatim in spirit and re-home it onto the event log and
the BEAM substrate.

| Track | Who writes | From | Form | CW analogue |
|---|---|---|---|---|
| **A – Deterministic stats** | code only (a pure fold) | the event log (`01 §8.2`) | typed `CapabilityStats` rows | `memory_collect_stats`, `memory_rebuild_agent_stats`, `memory_rebuild_global_stats` |
| **B – Agent observations** | an analysis agent, *proposing* | run trace + Track A + existing memory | cited `Observation` records | `memory-analyst.md` (writes), `self-improver.md` (consumes) |

Hard rule, inherited from CW: **Track B agents MUST NOT write Track A.** In CW
the rule is "NEVER write or modify .json files"; here it is enforced by the API –
the observation-proposal endpoint cannot mutate stats, and stats are derived, not
authored. Stats are *folded from the log*, so they are replayable and tamper-evident;
observations are *proposed and validated*, so they are auditable and attributable.

The one upgrade over CW: Track A is sourced from the **canonical event log**
(`capability_finished`, `budget_charged`, verifier evals) rather than scraping
`results/*.json` out of a worker dir. Same statistics, authoritative source.

---

## 3. Ownership and storage

Memory is **OS-owned** (`01 §2`, §7: "memory storage/retrieval"). The BEAM
control plane owns storage, retrieval, ranking, attribution, supersession, and
privacy. The sandbox **never** connects to the store directly – it receives an
injected slice inbound and, if it needs more, reaches the store only *through the
broker* (§5). Logical layout:

```
tenant/<tenant_id>/
  stats/                       # Track A – derived, never agent-written
    <capability_ref>/<model>/<harness>/CapabilityStats   # rebuilt by fold
    _global/GlobalStats                                  # roll-up (cf. CW global stats.json)
  knowledge/                   # Track B – proposed, validated, versioned
    obs/<obs_id>               # active | superseded | tombstoned
    tomb/<supersession_id>     # append-only supersession ledger
    index/<vector>             # embeddings for similarity retrieval (active only)
system/                        # first-party (T0) knowledge; opt-in, curated, no tenant data
```

Storage MUST enforce `tenant_id` as a row-level partition key, not a ranking
hint (§7). Persistence is BEAM-side (event log is the system of record; stats and
the obs/tomb store are projections that can be rebuilt by replay).

---

## 4. Schemas

### 4.1 Track A – `CapabilityStats` (derived, code-only)

```
CapabilityStats {                         # = fold(events where scope matches)
  scope          : { tenant_id, capability_ref, model, harness }   # ranking keys
  runs           : u64
  pass_rate      : f32     # (PASS+SKIP)/runs    – cf. CW pass_rate
  fix_rate       : f32     # cf. CW fix_rate
  fail_rate      : f32
  avg_iterations : f32     # cf. CW avg_iterations (loop-manager iters, 04)
  wall_ms        : { p50, p95 }
  usd            : { avg, p95 }   # from budget_charged Effect events (01 §8.2; 08)
  tokens         : { avg, p95 }
  verifier       : { avg_score, tier_pass_rate }   # tiered/checkpoint eval (04)
  first_seen, updated_at : iso8601
  source_runs    : [run_id]        # provenance → event log (replayable)
}
```

`GlobalStats` is the tenant roll-up (tasks analyzed, success rate, avg cost) –
the direct heir of CW `memory_rebuild_global_stats`. Both are **pure projections**:
delete them and a log replay reconstructs them exactly.

### 4.2 Track B – `Observation` (proposed, validated)

```
Observation {
  id          : uuid
  scope       : { tenant_id, capability_ref?, model?, harness? }   # nullable = broader applicability
  kind        : pattern | failure_mode | fix_strategy | decision | gotcha | escalation
  title       : string          # used as link text (cf. CW INDEX link text)
  body        : markdown        # qualitative, like CW patterns/*.md & analysis.md
  claim       : { subject, assertion }   # normalized for contradiction detection (§6)
  evidence    : [ run_id | event_seq | artifact_ref ]   # MUST be non-empty (see validate, §6)
  embedding   : vector
  confidence  : f32             # author-proposed, validator-adjusted
  status      : active | superseded | tombstoned
  supersedes  : [obs_id]        # set at write time by contradiction/merge (§6)
  version     : u32
  proposed_by : agent_ref       # memory-analyst | self-improver | orchestrator
  decay       : { last_used_at, use_count, recency_score }   # SECONDARY signal (§6)
  created_at, updated_at : iso8601
}
```

`escalation` is the heir of CW `ESCALATED.md` – a structural issue the agent
system cannot self-resolve; it is injected with high priority and surfaced to the
tenant, never silently superseded.

### 4.3 The supersession record (tombstone, auditable)

```
Supersession {                  # append-only; one per forgetting decision
  id             : uuid
  ts             : iso8601
  tenant_id      : string
  superseded_id  : obs_id              # the loser
  superseded_by  : obs_id | null       # winner (null = pure tombstone: decay/privacy)
  reason         : contradiction | merge | manual | decay | privacy_delete
  similarity     : f32                 # retrieval score that triggered detection
  contradiction  : { subject, old_assertion, new_assertion } | null
  decided_by     : "validator" | agent_ref
  effect_seq     : u64                 # the memory_written Effect this came from (01 §8.2)
}
```

Supersession is **never** a hard delete (except `privacy_delete`, §7). The loser
flips to `status=superseded`, leaves the active index, but stays readable for
audit and replay. Versioned + tombstoned ⇒ the full decision history of any claim
is reconstructable.

---

## 5. Retrieval, ranking, injection

**Retrieval.** k-NN over the *active* embedding index, filtered `tenant_id` first
(MUST), then scored:

```
score = w_sim·sim + w_scope·scope_match + w_conf·confidence
        + w_rec·recency_decay − w_stale·staleness
scope_match: exact(tenant,cap,model) > (tenant,cap) > (tenant)   # most-specific wins
```

Track A joins in: ranking SHOULD down-weight observations attached to a
(capability, model) whose `pass_rate` is low, and prefer those whose linked
configuration verifiably performs. Superseded/tombstoned entries are excluded.

**Injection (inbound, hermetic – `01 §8.5`).** At `prepare`, mitos selects the
top-N slices for `(tenant, capability, model)` within a token budget and writes
them into the **curated context slices** that `01 §8.5` already enumerates as a
legitimate inbound. The capability sees a read-only knowledge digest (a
`KNOWLEDGE.md`-shaped slice), **not** the store, **no** secrets, **no** other
tenants. This is the hermetic default (`01 §10.8`: "mitos owns memory, not the
harness").

**Mid-run dynamic fetch (outbound, through the broker).** If a harness needs more
than was injected, the request leaves the sandbox the *only* way anything does –
egress to the **broker** (`01 §8.5`). The broker calls the memory retrieval API
server-side (tenant context attached by the broker, never by the sandbox),
returns a slice, and logs the crossing as a `tool_brokered` Effect. The sandbox
never holds a store credential; deny-all-except-broker still holds.

---

## 6. Forgetting = write-time contradiction-supersession (decided)

The decided model: **we do not blind-append.** On every write we retrieve, and if
the new entry *contradicts* a prior one, the prior is superseded – not silently
shadowed by recency, not left to rot. Recency/usage **decay is a secondary
signal** that only reorders retrieval and breaks ties; it never deletes.

### Write path

```
propose(entry)                          # Track-B agent proposes (cannot persist directly)
  │
  ├─ validate(entry):                   # mitos, deterministic gate – REJECT if:
  │     · evidence empty                 (CW: "only changes supported by evidence")
  │     · evidence references another tenant's run   (cross-tenant guard, §7)
  │     · malformed claim / over size budget
  │
  ├─ neighbors = kNN(entry.embedding, scope=entry.scope, status=active)
  │
  ├─ for n in neighbors where sim(entry,n) ≥ τ_sim:
  │     c = classify(entry, n)          # agree | refine | contradict | unrelated
  │     match c:
  │       contradict → supersede(n, by=entry, reason=contradiction)   # n loses
  │       refine     → merged = merge(n, entry); supersede(n, by=merged, reason=merge)
  │       agree      → n.confidence↑ ; n.decay.use_count++ ; DROP entry   # dedup
  │       unrelated  → continue
  │
  ├─ if entry not consumed by an agree/merge → persist as new active obs
  │
  └─ emit memory_written Effect (01 §8.2)   ref={obs_id, op, supersedes:[...]}
```

`classify` answers one question on the normalized `claim`: *same subject, opposite
assertion?* (e.g. `subject="lib X for task Y"`: old `assertion="use"`, new
`assertion="banned, breaks Z"` ⇒ **contradict**). It MAY be a cheap rule for
structured decisions and a small LLM call for prose; it MUST cite which fields
conflicted, and that goes into `Supersession.contradiction`.

### Decay (secondary, never deletes)

`recency_score` and `use_count` feed `score` in §5 and break ties. A cold entry
ranks lower and may fall out of the injected top-N, but it stays `active` and
retrievable. Hard deletion happens **only** via `privacy_delete` (§7). This keeps
forgetting *semantic* (driven by contradiction) rather than *temporal* (driven by
a TTL), which is the whole point of the decision.

### Auditability

Every forgetting decision is a `Supersession` row + a `memory_written` Effect on
the canonical log. "Why did the system stop believing X?" is answerable by reading
the tombstone chain: which entry superseded it, on what similarity, citing what
contradiction, decided by whom, at which `effect_seq`.

---

## 7. The self-improvement loop

Post-run, the **tiered/checkpoint verifier's evaluation** (`04`) and **Track A
stats** flow into the knowledge service; an analysis pass distills the result and
populates the **per-model skill-adaptation seam** that `03` defines
(`resolved = base ⊕ per_model_adapt(model)`, `01 §5`). This is CW's
`memory-analyst → self-improver` arc, re-aimed from "edit CLAUDE.md" at the
*per-model skill seam*.

```
        ┌──────────────────────── RUN (capability, model) ─────────────────────────┐
        │  loop-manager iters · harness · verifier checkpoints (04)                 │
        └───────────────┬──────────────────────────────────────────────────────────┘
                        │ event log: capability_finished · budget_charged · verifier eval
          ┌─────────────┴──────────────┐
          ▼                            ▼
 ┌── TRACK A (code) ──┐      ┌── TRACK B (agent) ──────────────┐
 │ fold → Capability  │      │ memory-analyst reads run+stats, │
 │ Stats: pass/fix/   │      │ PROPOSES cited Observations →   │
 │ fail · p95 wall/$  │      │ validate → contradiction-       │
 └─────────┬──────────┘      │ supersession (§6) → persist     │
           │                 └─────────────┬───────────────────┘
           └──────────────┬────────────────┘
                          ▼
              KNOWLEDGE SERVICE  (store · rank · supersede · attribute)
                          │  distill  (self-improver pass; uses verifier signal, 04)
                          ▼
            per-model skill-adaptation candidates  ──►  SEAM (03): base ⊕ per_model_adapt(model)
                          │
                          ▼
        better / cheaper FUTURE runs  ──────────────────────► (back to RUN)
```

Boundary with `03`/`04`: the knowledge service is the **source of the distilled
signal and the candidate adaptation**; it does **not** own the adaptation
*generator* – `01 §5` marks that "generator out of scope," and `03` owns the seam
contract and `04` owns verify+distill. We produce evidence-backed proposals and a
ranked corpus; `03`/`04` decide how a candidate becomes a resolved skill. Each
adopted adaptation is itself evidence-linked back to the runs that justified it.

---

## 8. Tenancy and privacy

| Concern | Rule |
|---|---|
| Scope | every `CapabilityStats`, `Observation`, `Supersession` carries `tenant_id`; entries also scope to `capability_ref` (and optionally `model`/`harness`). |
| Isolation | retrieval, injection, and storage MUST filter `tenant_id` at the partition level (MUST, not best-effort). No cross-tenant entry is ever injected or ranked. |
| Write guard | `validate` (§6) REJECTS any proposal whose evidence references another tenant's `run_id`/`event_seq` – blocks leakage at authoring time. |
| First-party | the `system/` (T0) corpus MAY be injected to all tenants; it is curated, opt-in, and contains **no** tenant-derived data. |
| Seller capabilities (`08`) | a seller capability's observations default to the **buyer** tenant that produced them. Marketplace-quality roll-ups across buyers MUST be k-anonymized / counts-only (Track A aggregates), never raw Track-B observations. |
| Right-to-delete | a tenant delete cascades: write `Supersession{reason=privacy_delete, superseded_by=null}`, purge `body`+`embedding`, drop from the active index. Track A is re-folded from retained anonymized counters or purged per policy. |
| Replay honesty | a privacy delete breaks exact replay of an injected slice; record a redaction marker so replay is honest that context was removed rather than silently divergent (`01 §4` crash/replay discipline). |

---

## 9. Conformance checklist

- [ ] DATA (pillar) and KNOWLEDGE (service) are never conflated; DATA detail is delegated to `03`/`05`.
- [ ] Track A is code-derived from the event log only; Track B agents cannot mutate Track A (CW rule).
- [ ] Every persist emits a `memory_written` Effect (`01 §8.2`); every forget emits a `Supersession`.
- [ ] Injection is inbound/hermetic (`01 §8.5`); mid-run fetch is broker-only; sandbox holds no store credential.
- [ ] Forgetting is write-time contradiction-supersession; decay is secondary and never deletes (only `privacy_delete` does).
- [ ] Supersession is tombstoned + versioned + auditable to an `effect_seq`.
- [ ] `tenant_id` is a partition key; cross-tenant injection and cross-tenant evidence are both impossible.
- [ ] The self-improvement loop feeds the `03` per-model seam without owning the generator.

---

## 10. Open questions (decisions to verify)

1. **`classify` cost/placement.** Cheap-rule vs small-LLM split, and whether it
   runs inside the Track-B analysis agent or as a separate validated OS step.
2. **`τ_sim` and decay weights** (`w_*`) – tuning, and whether they are per-tenant
   configurable or fixed substrate constants.
3. **Distill cadence.** Per-run vs batched/scheduled (`06`) – CW queues workers
   (`pending.list`) and processes async; do we mirror that, or distill inline post-run?
4. **Stats sink.** `CapabilityStats` as a live BEAM projection vs a periodic
   rebuild job; interaction with metering (`08`) which reads the same Effect events.
5. **Right-to-delete vs replay.** How aggressively to purge Track A counters when
   exact-replay fidelity and privacy conflict.
