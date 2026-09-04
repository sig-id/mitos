# 04 — Control Plane

Elaborates `01_ARCHITECTURE` §4 (control model) and §8.1/§8.2 (Decision/Effect
events). This document fixes the two agentic control layers, the event-sourced
state they produce, and the markdown pipeline-intent format the orchestrator
reads. Field names from `01` §8 are canonical and MUST NOT be renamed here; this
spec only enumerates and constrains them. RFC 2119 keywords are normative.

Where this diverges from chief-wiggum it is cited: the jump-FSM of
`chief-wiggum/spec/PIPELINE_SCHEMA.md` and the Ralph loop of
`chief-wiggum/lib/runtime/runtime-loop.sh` are both **replaced**, not wrapped.

---

## 1. The model in one paragraph

**Agentic policy, deterministic record.** The orchestrator agent decides *what
happens next*; it can act on the world only by emitting a typed **Decision
event** (`01` §8.1). The substrate executes that decision through controlled
mechanisms and appends the resulting **Effect events** (`01` §8.2). The
append-only, ordered union of Decision + Effect events **is** the run state.

```
state(t) = fold(events[0..t])                # no separately-stored run state exists
resume   = replay events to the last committed Effect, then re-invoke orchestrator
```

A **run** is one execution of a pipeline against a **Task** dequeued from the work pool
(`06`; canonical Task shape `01` §8.7). A Task may produce several runs over its life
(retry / resume / branch); `run.status` (execution) is distinct from `task.status` (queue).
A run may also **park** awaiting an `invoke`'d subtask's result (`06` §5), resuming when
the subtask returns.

There is no authored finite-state machine (contrast chief-wiggum's pipeline of
`steps` with `on_result`/`jump`/`max` — a hand-written graph). The pipeline is
*enforced* by reasoning over the live log, never *pre-declared* as transitions.
Determinism lives in the **record and the limits**, not in the next move.

---

## 2. Two control layers, no third

| | **Orchestrator** | **Loop-manager** |
|---|---|---|
| Scope | cross-step (the whole pipeline) | intra-step (one capability's iterations) |
| Nature | agent (free-form reasoning) | **deterministic FSM** |
| Model use | every turn (one call → one Decision) | only on `escalate`/`switch` (one cheap meta-call) |
| Emits | all 9 Decision `type`s | only `switch`, `escalate` Decisions |
| `actor` field | `"orchestrator"` | `"loop_manager"` |
| Replaces | chief-wiggum jump-FSM dispatcher | chief-wiggum Ralph loop |

Determinism where the decision space is small (the loop-manager is coded);
agency where it is large (the orchestrator reasons over an open pipeline).

```
            pipeline intent (markdown) ─┐
            memory slices (07) ─────────┤
            budget remaining ───────────┤
            run state = fold(log) ──────┤
                                        ▼
   ┌──────────────────────── ORCHESTRATOR (agent) ───────────────────────┐
   │  emits ONE Decision per turn → substrate executes → Effects appended │
   └───────────────────────────────┬─────────────────────────────────────┘
            advance/insert/retry/skip│ (opens a step, grants budget)
                                     ▼
   ┌──────────────────────── LOOP-MANAGER (FSM) ─────────────────────────┐
   │  continue ▸ compact-context ▸ switch ▸ escalate ▸ done               │
   │  drives N iterations of ONE capability; Effects caused_by the        │
   │  opening Decision; emits `switch`/`escalate` Decisions only          │
   └───────────────────────────────┬─────────────────────────────────────┘
                                    ▼  HARNESS in a sandbox (05)
                          soft result + evidence (01 §8.3)
```

---

## 3. Orchestrator agent (cross-step)

One agent. Routing/dispatch is folded in — there is **no separate routing LLM**
(contrast chief-wiggum's `system.triage` agent that returns a `gate_result`
string consumed by `on_result` jumps). The orchestrator *is* the router.

### 3.1 Inputs (what the substrate assembles each turn)

| Input | Source | Notes |
|---|---|---|
| pipeline intent | the markdown doc (§8) | goal, stages, constraints, allowed-capabilities, success criteria |
| live run state | `fold(event log)` | which stages ran, their soft results + evidence, prior Decisions |
| memory slices | retrieval (`07`) | only slices relevant to the next move; never the whole store |
| budget | `budget.remaining` of last Decision | tokens / usd / wall_ms (`01` §8.1) |

The orchestrator never reads raw harness transcripts; it reads the **folded
state** (results, evidence refs, Effect summaries). Evidence is fetched on
demand by reference, not streamed into context.

### 3.2 Output — ONLY Decision events

The orchestrator's sole side effect is appending one `DecisionEvent`. It MUST
NOT mutate workspace, call tools, or write artifacts directly. `rationale` is
audit-only NL and MUST NOT be parsed to control flow (`01` §8.1).

### 3.3 Detecting "stuck" and adapting

The orchestrator folds the log to spot where a capability is failing and adapts.
The substrate also raises a `loop_manager` `escalate` Decision into the log when
intra-step signals trip (§4.3) — the orchestrator treats that as a first-class
input. Adaptation moves, expressed as Decision `type`s:

| Symptom (folded from log + evidence) | Move | Decision |
|---|---|---|
| step verified, success criteria met | proceed | `advance` |
| step produced weak result; precondition missing | inject a remediation/prep capability | `insert` |
| transient failure, evidence shows recoverable | re-run same capability with feedback | `retry` |
| capability is the wrong tool / a stronger one exists | change model/effort/harness/capability | `switch` |
| stage not applicable given upstream result | drop it | `skip` |
| high-stakes / monetized / policy gate | pause for human | `request_approval` |
| loop-manager exhausted its local ladder | take over the decision | (consumes `escalate`) |
| goal met | end the run successfully | `finish` |
| unrecoverable / budget exhausted / policy denial | end the run as failed | `abort` |

This subsumes chief-wiggum's entire `on_result`/`default_jump`/`on_max` table:
`jump:next`→`advance`, `jump:prev`/inline-fix→`retry`/`insert`,
`jump:abort`→`abort`, `on_max`→an orchestrator budget/iteration judgment rather
than a pre-wired target. There is no `max`/`on_max` to author — bounding is the
budget (§11) plus loop-manager signals (§4).

### 3.4 Control loop (deterministic harness around an agent)

```
loop:
  state   ← fold(log)
  if terminal(state): stop
  ctx     ← assemble(intent, state, memory_slices, budget)   # det.
  d       ← orchestrator.decide(ctx)                          # 1 model call → Decision
  append(d)                                                   # det., durable BEFORE execute
  effects ← substrate.execute(d)                              # det. mechanism (05/07/08)
  append(effects)                                             # det.
```

The `append(d)` BEFORE `execute(d)` is what makes the crash test (§7) pass.

---

## 4. Loop-manager (intra-step deterministic FSM)

A coded FSM, **not** a free agent. It drives the iterations of a *single*
capability that the orchestrator opened with `advance`/`insert`/`retry`. Its job:
compose each iteration's context, run the harness, watch deterministic signals,
and climb a fixed escalation ladder — spending a model call *only* to choose a
rung. Contrast chief-wiggum, whose Ralph loop spends a model call **every**
iteration to summarize and a periodic supervisor call to vote CONTINUE/STOP/
RESTART (`runtime-loop.sh` PHASE 2 + SUPERVISOR PHASE).

### 4.1 States & transitions

```
        ┌──────────┐  signals clear & not done   ┌──────────┐
   ───▶ │ continue │ ──────────────────────────▶ │ continue │ ── …
        └────┬─────┘                              └──────────┘
             │ context near limit
             ▼
     ┌───────────────┐  compacted        back to ─┐
     │ compact-context│ ───────────────────────────┘  continue
     └───────────────┘
             │ escalation signal trips (§4.3)
             ▼
   ┌──────────────────┐  pick rung (ONE model call)
   │  ESCALATE-CHOICE │ ─┬─ switch(effort/model/harness) → emit switch Decision → continue
   └──────────────────┘  └─ ladder exhausted → emit escalate Decision → return to orchestrator
             │ success criteria met / harness final_result
             ▼
        ┌──────┐
        │ done │  → return soft result + evidence; orchestrator re-invoked
        └──────┘
```

| State | Deterministic? | Emits | Effect(s) |
|---|---|---|---|
| `continue` | yes | — | `iteration_started`, `iteration_finished` |
| `compact-context` | yes (when to); model summarizes | — | `context_compacted` |
| `switch` | **1 model call to pick rung** | `switch` Decision (`actor=loop_manager`) | `harness_switched` |
| `escalate` | yes (signal) + that same pick | `escalate` Decision (`actor=loop_manager`) | `escalation_raised` |
| `done` | yes | — | `capability_finished` |

`iteration_*`, `context_compacted`, `harness_switched`, `escalation_raised`
extend the open Effect list of `01` §8.2 (its trailing `...`).

### 4.2 Context composition (each iteration)

The loop-manager **builds** the next context deterministically:

```
next_ctx = original_intent (verbatim, never summarized away)
         ⊕ compacted_trace   (only if compact-context ran; else running trace)
         ⊕ last_result + last_evidence_refs
         ⊕ escalation_feedback (if a switch just happened)
```

Compaction is **conditional** (fires only near the context limit), not
per-iteration. chief-wiggum summarizes unconditionally every loop and re-injects
the summary — paying a full model call each turn whether or not context is tight.
We keep the original intent verbatim so compaction can never erase the goal.

### 4.3 Deterministic escalation signals

These are computed from Effects + workspace diffs + token meter — **no model**.
When one trips, the FSM enters ESCALATE-CHOICE.

| Signal | Trip condition (defaults; tunable per capability) | First response |
|---|---|---|
| `no_progress` | no workspace diff AND no new artifact AND no result-state change for **N=3** iterations | compact-context, then climb |
| `repeated_error` | same tool/exec error class **M=2** times in a row | climb (effort→model→harness) |
| `budget_burn` | projected spend at current burn-rate exceeds `budget.remaining` before success criteria | escalate (orchestrator owns budget) |
| `verify_fail` | deterministic verifier (§10) fails **K=2** times after a work phase | climb, then escalate |
| `turn_exhaustion` | harness hit `max_turns` with no `final_result` | continue with carried context, bump turns once, else climb |
| `oscillation` | result-state cycles (edit↔revert detected in diffs) | escalate (needs a different capability) |
| `blocked_on_human` | `quiet(tree, T_block)` — no PTY bytes, no workspace writes, no *distinct* new exec — while the step is open (`14 §2`) | classify the modal state (`14 §3.2`) and raise a decision card (`15`); on `:unknown`, escalate as `judgment` |
| `predicate_stall` | acceptance-predicate delta ≈ 0 across **K=3** consecutive segments, regardless of diff volume (`16 §3.2`) | rewind to last green (`16 §3.3`) and retry with a failure memo; on repeat, escalate |

The first six signals are computed at **iteration boundaries**. The last two are
not, and that is the point: an interactive harness parked at its own permission
prompt never returns, so no boundary is ever reached and the first six can never
fire. `blocked_on_human` runs off the quiescence clock (`14 §2`) independently of
the loop; `predicate_stall` runs off the acceptance predicate, because
`no_progress` is diff-based and an agent that keeps editing always looks busy.

`repeated_error` generalizes chief-wiggum's deterministic **fast-fail** (the only
non-model bail-out in `runtime-loop.sh`: K consecutive sub-threshold failures).
We keep that instinct and add the other five signals plus a real ladder.

### 4.4 The escalation ladder (the only agency in the loop-manager)

```
effort↑  →  model↑  →  harness-switch  →  escalate-to-orchestrator
```

On a trip, the loop-manager spends **one cheap model call** ("ESCALATE-CHOICE")
to pick the next rung: which effort/model/harness to climb to, or whether the
problem is out of intra-step scope and must go up. The choice rationale is
recorded in the Decision's `rationale`. Climbing a rung emits a `switch`
Decision (`params: {model, effort, harness}` per `01` §8.1); exhausting the
ladder emits an `escalate` Decision whose `params` carry the tripped `signal`
and the climb history. The orchestrator then owns the next move (§3.3).

**The outer bound: restart intensity, not per-signal counters.** The N/M/K above
are *first responses*; alone they let a run retry indefinitely by rotating which
signal trips. The ladder is therefore bounded by OTP supervision semantics —
`max_restarts` within `max_seconds`, enforced per step **and** per run, with a
`strategy` of `retry_step | rewind_to_green | rewind_tail | escalate`
(`16 §4`). Exhausting intensity at either level escalates to a human as
`judgment` (`15 §2`); it never silently keeps climbing.

This is the explicit divergence from chief-wiggum's "summarize-and-reloop": its
supervisor can only vote CONTINUE / STOP / RESTART on the *same* model and
harness; it cannot climb effort/model/harness, and it pays a model call every
iteration regardless. Our loop is silent until a deterministic signal trips.

---

## 5. Decision events (elaborate `01` §8.1)

```
DecisionEvent {
  run_id      : string
  seq         : u64                  # monotonic per run (single writer, §7)
  ts          : iso8601
  actor       : "orchestrator" | "loop_manager" | "supervisor"
  type        : advance | insert | retry | switch | skip
              | request_approval | escalate | finish | abort | rewind
  target      : capability_ref | stage_id | checkpoint_id | null
  params      : object               # type-specific (below)
  inputs_hash : string               # hash of fold(log)+intent the decision saw (replay integrity)
  rationale   : string               # NL — AUDIT ONLY, never parsed for control
  budget      : { tokens, usd, wall_ms, remaining }   # carried on EVERY decision
}
```

| `type` | actor | `target` | `params` |
|---|---|---|---|
| `advance` | orch | next `stage_id` / `capability_ref` | `{ inputs?, success_criteria_ref }` — open the step, grant its budget slice |
| `insert` | orch | new `capability_ref` | `{ position: before\|after, reason_signal? }` |
| `retry` | orch | `stage_id` | `{ feedback, reset: workspace\|context\|none }` |
| `switch` | orch \| loop | `capability_ref` / null | `{ model?, effort?, harness?, capability? }` |
| `skip` | orch | `stage_id` | `{ reason }` |
| `request_approval` | orch | `stage_id` / null | `{ question, options?, blocking: true, stakes }` |
| `escalate` | loop (→orch) \| orch (→operator) | `stage_id` | `{ signal, climb_history, suggestion? }` |
| `finish` | orch | null | `{ result_ref, success_criteria_met: bool }` |
| `abort` | orch | null | `{ cause: budget\|policy\|unrecoverable, detail }` |
| `rewind` | orch \| supervisor | `checkpoint_id` | `{ scope: step\|tail, memo, reason_signal, allow_non_green? }` — restore a checkpoint and re-open (`16 §3.3`); `memo` REQUIRED |

**`actor="supervisor"`** is a fleet supervisor acting on *another* run
(`16 §5`). It is restricted to `retry · switch · rewind · escalate · abort ·
request_approval`, and MUST NOT emit `advance`, `insert`, `skip`, or `finish` —
a supervisor that can declare another run finished can declare it finished
wrongly. It reads that run's Decisions, Effects, diffs, and predicate verdicts;
it MUST NOT read its `assistant_text`, inner trace, raw spool, or `rationale`
(`16 §5.1`) — the same independence §3.1 already requires of the orchestrator
over its own run, applied one level up.

A `switch` from `actor="loop_manager"` is an intra-step climb (§4.4); from
`"orchestrator"` it is a cross-step substitution. Same `type`, different scope —
disambiguated by `actor` + `target`, never renamed.

---

## 6. Effect events (elaborate `01` §8.2)

```
EffectEvent {
  run_id, seq, ts,
  type      : capability_started | capability_finished | tool_brokered
            | egress_allowed | egress_denied | artifact_registered | commit
            | approval_requested | approval_resolved | memory_written
            | budget_charged
            # control-plane extensions (the §8.2 open list):
            | iteration_started | iteration_finished | context_compacted
            | harness_switched | escalation_raised | verification_recorded
            | quiescent | steer_applied | steer_rejected | harness_quarantined   # 14
            | decision_delivered | decision_defaulted | decision_rejected        # 15
            | checkpoint_taken | oracle_tampered                                 # 16
  caused_by : seq                    # the DecisionEvent that led here (causality)
  ref       : object                 # ids / paths (artifact ids, sandbox handle, commit sha)
  payload   : object                 # type-specific body
}
```

| Effect | `caused_by` (typical) | Carries |
|---|---|---|
| `capability_started` | `advance`/`insert`/`retry` | sandbox handle, harness, model, trust_tier |
| `iteration_started`/`iteration_finished` | same opening Decision | iteration idx, turns, diff stat |
| `context_compacted` | opening Decision | pre/post token counts, summary artifact ref |
| `tool_brokered` | the iteration's opening Decision | tool name, schema id |
| `egress_allowed`/`egress_denied` | opening Decision | dest, credential id (never the secret), policy rule |
| `artifact_registered` | opening Decision | artifact id, path, kind |
| `commit` | opening Decision (or post-hook) | sha, files |
| `harness_switched` | `switch` | from→to harness/model/effort |
| `escalation_raised` | `escalate` | signal, climb_history |
| `capability_finished` | opening Decision | soft result + evidence refs (`01` §8.3) |
| `verification_recorded` | `advance`/`finish` (a verifier step) | tier, verdict, evidence judgment, distill_ref (§10) |
| `approval_requested`/`approval_resolved` | `request_approval` | question, resolver, outcome |
| `memory_written` | `finish`/post-hook | track, slice id (`07`) |
| `budget_charged` | any Decision that spent | tokens, usd, wall_ms delta |
| `quiescent` | opening Decision | `for_ms`, `pty_offset`, writes/execs since (`14 §2`) |
| `steer_applied`/`steer_rejected` | the steering Decision | before/after grid digests, command (`14 §3.3`) |
| `checkpoint_taken` | step-opening Decision | commit sha, snapshot ref, log seq, predicate verdict, `green` (`16 §3.1`) |
| `oracle_tampered` | opening Decision | the diff paths intersecting `acceptance.oracle_paths` (`16 §3.2`) |
| `decision_delivered`/`decision_defaulted`/`decision_rejected` | `request_approval` | sink/connector, resolver, `via` (`15 §5`) |

**The log is five things at once** (`01` §8.2): run state (fold it), debug trace
(read it), replay source (re-drive it, §7), audit record (`rationale` +
causality), and billing ledger (`budget_charged` → `08`). One append-only
structure; no parallel bookkeeping that can drift from it.

---

## 7. Resume / replay

State is never stored separately, so recovery is replay, not reconstruction.

```
on_recover(run_id):
  log    ← read_log(run_id)                     # ordered by seq
  last   ← max seq with a committed Effect
  state  ← fold(log[0..last])                    # deterministic
  pending_decisions ← decisions with seq > last and no Effect   # at most the tail
  re-drive(pending_decisions)                    # idempotent (below); OR re-invoke orchestrator
```

**Crash test (`01` §4, MUST pass).** Process dies after a Decision is appended
but before its Effect commits:

```
seq 41  Decision  advance(target=stage:test)   ✔ on log
seq 42  Effect    capability_started            ✘ never committed  ← crash here
```

Recovery sees Decision 41 with no Effect. It does not guess and it does not
re-decide — it **re-drives** decision 41. Because the Decision is total and the
Effect is not, the action simply runs again. chief-wiggum resumes from
`pipeline-config.json.current.step_idx` (a mutable pointer that can itself be
half-written); we resume from the immutable log, so there is no torn-pointer
case.

### 7.1 Idempotency of re-driven Effects

Re-driving MUST converge. Each executable Decision carries enough identity that
its Effects are keyed and deduplicated:

| Mechanism | Effect | Re-drive behaviour |
|---|---|---|
| sandbox launch keyed by `(run_id, seq)` | `capability_started` | second launch reuses/replaces the orphaned sandbox; one `started` wins |
| broker crossings keyed by `(seq, dest, nonce)` | `egress_allowed` | duplicate suppressed; **never double-charges credentials** |
| artifacts content-addressed | `artifact_registered` | same id ⇒ no-op |
| `commit` checks workspace sha first | `commit` | if already at target sha, no-op |
| `budget_charged` keyed by `caused_by` seq | `budget_charged` | charge applied at most once per Decision |

Effects with external, non-idempotent side effects (a real outbound POST through
the broker) MUST record an intent Effect *before* the crossing and a completion
Effect *after*, so re-drive can detect "already attempted" and require approval
to repeat. Non-idempotent egress without that bracket MUST be treated as
at-most-once and surfaced to the orchestrator, not silently retried.

---

## 8. Pipeline intent in markdown (loose format)

**Decided: pipelines are authored in markdown, loose format.** A pipeline file
is a markdown doc the orchestrator *reads*; intent may be a fenced YAML/JSON
block, a table, or prose. There is **no rigid schema validator** and no compile
step (contrast chief-wiggum's `pipeline.json` validated into a step array).
Strong docs + canonical examples carry the format; intelligent
instruction-following does the rest. The orchestrator is told: "this is the
operator's intent; honor it; adapt only as needed and record why."

### 8.1 Fields a pipeline intent SHOULD contain

| Field | Purpose | If omitted |
|---|---|---|
| `goal` | the outcome, in plain language | orchestrator cannot run — the one near-required field |
| `stages` / `capabilities` | the intended sequence (names + one-line intent each) | orchestrator proposes its own ordering |
| `allowed_capabilities` | the set it MAY draw from (incl. for `insert`) | defaults to the tenant's catalog |
| `constraints` | hard rules (no network, no force-push, data residency, T-tier) | treated as unconstrained — discouraged |
| `budget` | tokens / usd / wall_ms ceiling for the run | tenant/plan default applied |
| `success_criteria` | how "done" is judged (tests pass, checks, human ok) | orchestrator infers from goal; weaker verify |
| `checkpoints` | where the smart verifier (§10) MUST run | only deterministic checks run |

`constraints` and `allowed_capabilities` are **enforced** (compiled into launch
policy at `05` and budget at `08`); the rest is *guidance* the orchestrator
interprets. Soft guidance vs hard limit is the whole architecture in miniature.

### 8.2 Example — code-feature pipeline

````markdown
# Pipeline: add-feature

Goal: implement the GitHub issue in `$ISSUE_URL` on this repo, with tests,
and open a PR. Prefer the smallest change that satisfies the issue.

```yaml
budget: { usd: 8, wall_ms: 3600000 }
constraints:
  - no force-push, no history rewrite
  - branch from main; never commit to main
  - network: broker-only (T2)
allowed_capabilities: [planner, code-edit, test-runner, pr-author]
success_criteria:
  - test-runner reports all tests green
  - a PR exists targeting main
checkpoints: [after:test-runner]
```

## Stages
1. **planner** — read the issue + repo, write a short plan. (read-only)
2. **code-edit** — implement the plan. Iterate until tests pass locally.
3. **test-runner** — run the suite; on failure feed diffs back to code-edit.
4. **pr-author** — open a PR summarizing the change and linking the issue.

Notes: if the issue is ambiguous, request_approval before large edits.
````

### 8.3 Example — design-job pipeline

````markdown
# Pipeline: brand-landing

Goal: produce three distinct landing-page design directions for the brief in
`brief.md`, then refine the one the reviewer picks into a responsive mockup.

constraints: stay within the brand palette in `/data/brand/tokens.json`.
budget: 30 USD. allowed: [moodboard, layout-gen, design-critic, mockup-builder].
success: a reviewer-approved mockup artifact + a one-page rationale.
checkpoints: after the critic, and before final mockup (high-stakes, monetized).

Stages (prose):
First run **moodboard** to fix visual language from the brief and brand tokens.
Then **layout-gen** three times for three directions — these MAY run
concurrently (independent). **design-critic** scores them against the brief;
**request_approval** so a human picks one. Finally **mockup-builder** refines
the pick. If the critic rejects all three, insert another layout-gen round
rather than forcing a weak pick.
````

Both files are valid. The first leans structured (YAML fence), the second leans
prose — the orchestrator reads either. Tooling MAY lint for the recommended
fields and warn, but MUST NOT reject a pipeline for shape.

---

## 9. Plan → Work → Verify

The default *pipeline* shape is **plan → work → verify** as distinct stages
(capabilities), which the orchestrator advances through and adapts. Separately,
a single capability's *runtime* MAY self-plan inside its step — the loop-manager
does not forbid an internal plan/act loop within the harness. Two levels:

```
pipeline:   plan(cap) ──advance──▶ work(cap) ──advance──▶ verify(cap)   [orchestrator]
                                      │
within a step:        the capability's harness may plan→act→self-check   [loop-manager]
```

Plan stages SHOULD be read-only (no workspace mutation; cf. chief-wiggum's
`readonly` checkpoint) so a re-plan is cheap and side-effect-free.

---

## 10. Verification (tiered + checkpoint)

**Decided: verification is tiered, and the smart tier runs only at checkpoints.**

| Tier | When | What | Cost |
|---|---|---|---|
| **Deterministic / heuristic** | **always**, after every work phase | tests, linters, schema/contract checks, diff sanity, the §4.3 signals | cheap, no model |
| **Smart-model verifier** | only at pipeline `checkpoints`, high-stakes, or **monetized** runs | a capability that interprets the soft result + evidence (`01` §8.3) and judges against `success_criteria` | ~doubles agents — gated |

The deterministic tier feeds the loop-manager's `verify_fail` signal directly.
The smart verifier is itself a capability (a T0 helper) emitting a soft verdict;
its result is written as a `verification_recorded` Effect.

**Reuse of the verifier output.** The smart verifier's interpretation +
evidence judgment is the *same* signal self-improvement needs, so it is captured
once and reused:

```
verification_recorded.payload  ──▶ memory distillation (07)   # what worked / failed, why
                               └──▶ per-model skill-adapt seam (03 §5):
                                     resolved = base ⊕ per_model_adapt(model)
```

The verdict (`distill_ref`) seeds the per-model skill adaptation for the model
that did the work, and a memory write for the capability. **Cost rationale:**
independent verification roughly doubles the agent count per step; running the
smart tier only at checkpoints / high-stakes / monetized runs controls that,
while the always-on deterministic tier keeps every step honest for free.

---

## 11. Budgets & concurrency

**Budget is on every Decision** (`01` §8.1 `budget: {tokens, usd, wall_ms,
remaining}`). The orchestrator MUST respect `remaining`; the substrate
debits via `budget_charged` Effects and refuses to execute a Decision whose
grant exceeds `remaining` (it emits `abort{cause:budget}` instead). The
loop-manager's `budget_burn` signal (§4.3) escalates *before* exhaustion so the
orchestrator can re-scope, `skip`, or `finish` rather than crash into the wall.

```
run budget ─▶ orchestrator partitions per advance ─▶ step slice ─▶ loop-manager spends
   ▲                                                                      │
   └──────────────── budget_charged Effects fold back ────────────────────┘
```

**Concurrency.** Independent stages MAY run in parallel (e.g. §8.3's three
`layout-gen`). Each concurrent capability runs in its own sandbox, but all
Effects funnel through **one per-run writer** (a BEAM process owning the run)
that assigns the monotonic `seq` — so the log stays a single totally-ordered
stream even under fan-out, and `fold` remains deterministic. Concurrent stages
draw from disjoint budget slices; the writer rejects a charge that would drive
`remaining` negative. Cross-run concurrency (multi-tenant) is isolated by
`run_id`-scoped logs and the multi-tenancy boundary of `08`.

---

## 12. Judgment calls (flagged for review)

1. **Loop-manager Decisions are sparse by design** — `continue`/`compact`/`done`
   produce only Effects, not Decisions; only `switch`/`escalate` are Decisions.
   This keeps the Decision log a policy-change log. Alternative: emit a Decision
   per iteration (heavier log, simpler causality).
2. **One ESCALATE-CHOICE model call per trip** is the only loop-manager agency.
   If even that is too much, the ladder could be fully fixed (no model) — cheaper
   but blind to *why* it's stuck.
3. **No schema validator for pipeline markdown** is deliberate (docs carry it).
   Risk: malformed intent fails late, at orchestration. Mitigation: a non-blocking
   linter (§8.3).
4. **Re-drive vs re-decide on recovery** — §7 prefers re-driving the pending
   Decision (cheaper, deterministic) over re-invoking the orchestrator. For
   non-idempotent egress it degrades to at-most-once + approval. Confirm that
   matches the broker semantics in `05`.
5. **`escalate` is overloaded** (loop→orch and orch→operator), disambiguated by
   `actor`. Kept to avoid adding a 10th Decision `type` to `01` §8.1.
