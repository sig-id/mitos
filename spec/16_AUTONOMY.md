# 16 — Autonomy Plane (Running Unattended)

What makes a run survive hours without a human. This doc owns the **unattended
contract**: stall detection (§2), checkpoints and rewind (§3), restart intensity
(§4), the fleet supervisor (§5), and the metrics that say whether any of it
works (§6).

It adds one Decision type (`rewind`), two escalation signals, and one manifest
block (`acceptance`). Everything else is a discipline over mechanisms `04`,
`05`, and `14` already own.

RFC 2119 keywords are normative.

---

## 0. The claim

> Effectiveness over long horizons comes from **making failure cheap and
> detectable**, not from making the agent smarter.

Every mechanism below is a variant of that sentence. Nothing here tries to make
a model more capable; each item shortens the distance between "went wrong" and
"known wrong", or between "known wrong" and "back on solid ground".

The corollary is a build rule: **do not run a twelve-hour task.** Run forty
twenty-minute segments, each ending green. Repair-in-place from a corrupted
state is how long runs die; rewind-to-green and re-attempt with a memo is how
they survive.

---

## 1. The autonomy stack

```
        acceptance predicate (§3.2)   ── the only ground truth
                  │
   progress = Δpredicate per segment  ── the only non-narrative progress metric
                  │
   ┌──────────────┴───────────────┐
   │ stall detection (§2)          │  quiescence clock (14 §2) + iteration signals (04 §4.3)
   │ checkpoint / rewind (§3)      │  cheap failure
   │ restart intensity (§4)        │  bounded retry, OTP semantics
   │ escalation (15)               │  when the substrate is out of moves
   └──────────────┬───────────────┘
                  │
        supervisor (§5) — evidence only, never the worker's context
                  │
        autonomy metrics (§6) — is any of this working?
```

---

## 2. Stall detection — the gap that blocks everything

`04 §4.3`'s six signals are all computed at **iteration boundaries**. An
interactive harness parked at its own permission prompt never returns, so no
boundary is reached, so `no_progress` never fires and the run hangs until a
human notices. Every autonomy claim dies here.

Two signals are added, both computed off the quiescence clock (`14 §2`) and the
acceptance predicate (§3.2) rather than off iteration boundaries:

| Signal | Trip condition | First response |
|---|---|---|
| `blocked_on_human` | `quiet(tree, T_block)` (`14 §2.1`) while the step is open | classify the modal state (`14 §3.2`); raise a decision card (`15`) — or, if `:unknown`, escalate as `judgment` |
| `predicate_stall` | acceptance-predicate delta ≈ 0 across **K=3** consecutive segments, regardless of diff volume or narration | rewind to last green (§3.3), retry with a failure memo; on repeat, escalate |

`predicate_stall` exists because `no_progress` is diff-based and therefore
trivially satisfied by an agent that keeps editing. Confident narration and a
busy diff are not progress; a moved predicate is.

`blocked_on_human` is the load-bearing one: it is harness-agnostic,
mode-agnostic, and requires understanding nothing about what is on screen. It is
the signal that makes "unattended" a measurable claim rather than a hope.

### 2.1 Ordering

`blocked_on_human` MUST be evaluated before the classifier, and the classifier's
result MUST NOT be required for the signal to fire. A stall detected with an
unreadable screen still escalates — with less information, on time.

---

## 3. Segments, checkpoints, rewind

### 3.1 Checkpoint

Every step boundary (`04 §3.3` `advance`/`insert`/`retry`) is a **checkpoint**,
taken at a quiescence edge (`14 §2.2`):

```
Checkpoint {
  checkpoint_id, run_id, seq,          # log offset — the fold prefix
  workspace_commit : sha,              # 05 §5.4 harvest surface, committed
  sandbox_snapshot : ref | :unsupported,   # 05 §8
  predicate        : { verdict: pass|fail|unknown, delta, evidence_ref },
  green            : bool              # verdict == pass
}
```

Emitted as a `checkpoint_taken` Effect. Providers without `snapshot` (`01 §8.4`
returns `:unsupported`) still checkpoint on commit + log offset; rewind then
replays from the commit rather than restoring a VM, which is slower and equally
correct.

### 3.2 The acceptance predicate — the only ground truth

`03`'s capability `result` is a **soft** result interpreted by the orchestrator
(`01 §8.3`). Over one step that is fine; over a twelve-hour horizon it is a
closed loop in which the model grades its own homework. Therefore the capability
manifest (`01 §8.3`) gains:

```
acceptance : {
  cmd          : <argv>            # machine-checkable; exit 0 = pass
  oracle_paths : [path, ...]       # what constitutes the oracle
  timeout_ms   : int
  measure      : <argv> | null     # optional: emits a scalar/vector for delta
}
```

Three normative rules:

1. **The predicate MUST be machine-checkable.** An LLM judge MAY *supplement* it
   (`04 §10` tiered verification) but MUST NOT be the sole verdict for a run
   granted an unattended horizon (§6).
2. **The predicate MUST execute outside the workspace.** Pristine checkout +
   the harvested patch + the oracle fetched from a path the sandbox never
   mounted writable. "Make the tests pass" and "make the test runner lie" are
   the same gradient until the oracle is out of reach.
3. **Touching the oracle is an event, not a judgment call.** If the harvested
   diff intersects `oracle_paths`, the substrate emits `oracle_tampered` and
   escalates as `judgment` (`15 §2`). This is a path check on a diff — cheap,
   deterministic, and it removes the most common way a long run produces a
   confidently wrong green.

**Progress is `Δpredicate` per segment.** That is the definition used by
`predicate_stall` (§2), by the supervisor (§5), and by the metrics (§6).

### 3.3 `rewind` — the tenth Decision type

`01 §8.1` gains `rewind`:

| `type` | actor | `target` | `params` |
|---|---|---|---|
| `rewind` | orch \| supervisor | `checkpoint_id` | `{ scope: step \| tail, memo, reason_signal }` |

- `scope: step` restores the workspace and re-opens that step (OTP
  `one_for_one`). `scope: tail` additionally invalidates every step that
  depended on it (`one_for_all` over the dependency tail, `06 §4`).
- `memo` is the failure memo carried forward into the retry's context
  (`04 §4.2`). A rewind without a memo repeats the failure; the substrate MUST
  reject one.
- Rewind **never rewrites the log.** It appends. The abandoned segment stays on
  the record, which is what makes attribution (§6.1) possible at all.
- `rewind` targeting a non-green checkpoint requires an explicit
  `allow_non_green` param; the default target is the last green.

---

## 4. Restart intensity — OTP semantics, not ad-hoc counters

The retry limits in `04 §4.3`/`§4.4` are per-signal counters (N=3, M=2, K=2)
with no run-level bound, so a run can retry indefinitely by rotating which
signal trips. Replace the bound with the supervision formalism the BEAM already
implements and has run at decade scale:

```
supervision : {
  max_restarts : int,          # default 3
  max_seconds  : int,          # default 3600 — restarts within this window
  strategy     : retry_step | rewind_to_green | rewind_tail | escalate
}
```

The mapping is exact, and it is why this is the right formalism rather than an
analogy:

| OTP | Here |
|---|---|
| supervisor never inspects the failed child's internals | the supervisor reads evidence, never the worker's context (§5) |
| `one_for_one` / `one_for_all` | `rewind{scope: step}` / `rewind{scope: tail}` |
| **restart intensity** — `max_restarts` in `max_seconds` | how many segment restarts before a human is worth interrupting |
| restart from a known-good `init` | rewind to the last **green** checkpoint, never repair forward |
| shutdown escalates to the parent | intensity exhausted ⇒ `escalate` ⇒ decision card (`15 §2`, kind `judgment`) |

Intensity is enforced per **step** and per **run**, and exhaustion at either
level escalates. The per-signal counters in `04 §4.3` remain as *first-response*
triggers; this is the outer bound they were missing.

---

## 5. The fleet supervisor

A supervisor is **a capability running in the substrate** (`03`), whose input is
the folded logs of other runs. Building it as a capability rather than a special
process means it is sandboxed, metered, replayable, and versioned like anything
else — and mitos supervises itself with its own machinery.

### 5.1 The independence rule (normative)

> A supervisor MUST NOT read the supervised run's `assistant_text`, inner trace
> (`11 §4`), raw spool, or `rationale`. It reads Decisions, Effects, diffs,
> predicate verdicts, and evidence refs.

`04 §3.1` already forbids this for the orchestrator over its own run; §5.1
extends it one level up. The reason is not tidiness: a supervisor that reads a
worker's narration inherits its delusions and rubber-stamps them. Two
independent views of the same run are the entire value of having a second
process look at it.

### 5.2 Powers

A restricted Decision set on *other* runs, each appended to the supervised run's
log with `actor: "supervisor"` (extending `01 §8.1`'s actor enum):

`retry` · `switch` · `rewind` · `escalate` · `abort` · `amend_brief` (via
`15 §3` `kind: amend_brief`) · `request_approval`.

It MUST NOT emit `advance`, `insert`, `skip`, or `finish` — those are the
orchestrator's judgment about its own pipeline, and a supervisor that can
declare another run finished can declare it finished wrongly.

### 5.3 Coordination between workers

- **Static ownership.** Each worker declares owned paths/modules; overlapping
  claims are a scheduling conflict (`06 §9.3`), refused at dispatch rather than
  discovered at merge.
- **Leases** for singleton resources (ports, dev databases, flaky external
  services), held on the log.
- **An explicit integration task** owns the merge, scheduled after siblings land
  (`06 §4`). Patches that individually pass and jointly break have no general
  solution; ownership plus an integration step is the mitigation, and a shared
  mutable blackboard is not attempted.

---

## 6. The metrics — is it working?

All computable from the existing log on day one, which means the product can be
measured before most of it is built.

| Metric | Definition | Target direction |
|---|---|---|
| **autonomy ratio** | productive wall-clock ÷ human decisions raised | ↑ |
| **unattended horizon** | p50 time from run start to first *required* decision (no default available) | ↑ |
| **decision precision** | fraction of raised cards **not** resolved as their default | ↑ |
| **rewind depth** | segments discarded per accepted change | ↓ |
| **green rate** | checkpoints with `green: true` ÷ checkpoints taken | ↑ |

**Decision precision is the self-tuning one.** A card that is always resolved as
its default should not have been raised; the escalation rule that raised it is
noise and SHOULD be deleted or its floor raised (`15 §7`). This is why
`decision_defaulted` is preserved distinctly on the log forever (`15 §4`).

The **unattended horizon** is also a *grant*, not only a measurement: a run
is granted a horizon (`unattended_until`), and a harness observable only through
C1+C3 (`14 §4`), or a capability with no machine-checkable predicate (§3.2), is
granted **zero** — it may be watched, not left.

### 6.1 Replay as eval — the compounding asset

Failure attribution — model vs. brief vs. environment — is unanswerable without
replay, and replay is nearly free here because the log is already
content-addressed and re-drivable (`01 §4`, `04 §7`).

Therefore: **the recorded run corpus is the regression suite.** A change to an
escalation threshold, an orchestrator prompt, a classifier, or a default policy
is replayed against N recorded runs before it ships. This adds one reader to
`02 §1`'s list — the buyer reads a proof, the platform reads a meter, the seller
reads attribution, the auditor reads the record, and **mitos's own CI reads the
corpus**. It is the reader no single-tenant framework grows, because it requires
runs worth replaying and a log designed for strangers to read.

---

## 7. Open questions

- **`T_block` per harness.** Seeded from `think_time_p95_ms` (`14 §4`), but a
  harness on a hard problem legitimately thinks longer than its p95. Whether to
  adapt the threshold within a run, or accept a premature card, is undecided.
- **Predicate for non-code capabilities.** A design or writing capability has no
  `exit 0`. Either those capabilities get zero unattended horizon, or the
  predicate admits a checked structural form (schema + assertions over the
  artifact). Undecided; default until settled is zero horizon.
- **Self-hosting correlated failure.** Mitos building mitos means a bad change
  breaks the thing building the fix. Supervisor and verifier MUST run from a
  pinned, separately-versioned mitos; the pinning mechanism is unspecified.
- **Rewind vs. amend-brief interaction.** See `15 §7`.

## Cross-references

- Iteration signals and the escalation ladder this bounds: `04 §4.3`, `§4.4`.
- Decision/Effect schema, `rewind`, `actor: supervisor`: `01 §8.1`, `§8.2`.
- Capability manifest and `acceptance`: `01 §8.3`, `03 §8`.
- Snapshot/resume mechanics behind checkpoints: `05 §8`.
- The quiescence clock: `14 §2`.
- Asking a human when the substrate is out of moves: `15`.
- Ownership, leases, dependency tail: `06 §4`, `§9.3`.
