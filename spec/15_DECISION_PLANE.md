# 15 – Decision Plane (Asking a Human, Rarely and Well)

Every place mitos needs a human, and the one shape that request takes. This doc
owns the **human decision contract**: when to ask (§2), what a card is (§3),
what silence means (§4), how it reaches a person (§5), and how the two products
(`02 §1`) share the mechanism with different policy (§6).

It does not introduce a new authority path. A human decision is consumed by the
substrate exactly like any other input to a Decision (`01 §8.1`); the agent
never asks anyone for anything.

RFC 2119 keywords are normative.

---

## 0. The gap

The substrate can already stop and ask: `request_approval` is a Decision type
(`01 §8.1`), `approval_requested` / `approval_resolved` are Effects (`01 §8.2`),
and the broker parks on `requires_approval` (`05 §5.3`). Three things are
missing, and each is fatal to unattended operation:

1. **Approvals are gated on *category*.** "Is this an egress? then ask." That
   rule asks constantly about reversible nothings and produces interrupt storms,
   which is the failure mode that makes long-horizon autonomy unusable.
2. **There is no default and no clock.** An unanswered approval parks forever.
   Every interrupt therefore becomes a stall, and the interrupt count you
   reduced becomes a stall count you did not.
3. **There is no transport.** Nothing specifies how a parked run reaches a
   person who is not looking at a terminal.

---

## 1. The governing principle

> **Escalate on irreversibility × uncertainty, never on category.**

Category ("it's a write", "it's an egress") is a property of the *mechanism*.
Irreversibility is a property of the *consequence*, and consequence is what a
human is actually being asked about. The corollary is the design's main
autonomy lever:

> **Blast-radius declaration buys autonomy – not a better model.** A run that
> structurally cannot reach anything outside its sandbox earns near-total
> freedom for free, because every mistake it can make is one `rewind` away
> (`16 §3`).

---

## 2. Reversibility classes

Computed **deterministically** by the substrate from the act and the run's
grants – never by a model, never from the agent's own claim about its intent.

| Class | Definition | Examples | Policy |
|---|---|---|---|
| `internal` | confined to the sandbox workspace; undone by `rewind` to a checkpoint (`16 §3`) | file edits, test runs, local commits, in-sandbox processes | **never escalate.** Log only. |
| `recoverable` | escapes the workspace but a checkpoint or a compensating action restores it | push to a mitos-owned mirror, writes outside the workspace but inside the sandbox, a draft PR | **act, log, queue for async review** (digest, §5.3) – do not push |
| `external` | visible outside mitos's control; assumed irreversible | upstream push, package publish, payment, message to a third party, prod mutation, any brokered egress with `requires_approval` | **always escalate. No default. Park.** |

Two kinds escalate regardless of class, because no policy can resolve them:

| Kind | Why it is unguessable |
|---|---|
| `missing_input` | a credential, an account, a fact only the human holds |
| `judgment` | a product/design/priority call the brief did not settle |

And one derived from `16`: a run that has burned its restart intensity, or whose
acceptance predicate has not moved for K segments, escalates as `judgment`
(`16 §4`) – the substrate has run out of moves, which is exactly when a human is
worth interrupting.

### 2.1 Determinism obligation

The class MUST be derivable from `(act, egress_policy, workspace bounds,
capability grants)` alone. Where it cannot be – an unclassifiable act, an
`:unknown` modal state (`14 §3.2`), a `steer_rejected` (`14 §3.3`) – the
substrate MUST treat it as `external`. **Unclassifiable is not permission.**

---

## 3. The decision card

Canonical shape (`01 §8.9`). Carried in the `approval_requested` Effect payload;
Effect names are unchanged.

```
HumanDecision {
  decision_id  : string
  run_id       : string
  caused_by    : seq                    # the request_approval / escalate Decision
  tenant       : { org, project }
  kind         : approve | choose | supply_value | amend_brief | rewind | abort
  reversibility: internal | recoverable | external
  question     : string                 # imperative, <= 200 chars, no transcript
  context      : { attempted, blocked_by, evidence_refs[] }   # FOLDED state only
  options      : [ { id, label, consequence, recommended?: bool } ]
  default      : option_id | null       # MUST be null when reversibility = external
  ttl_ms       : int | null             # MUST be null when default is null
  blast_radius : { paths[], destinations[], spend }
  cost_of_wrong: { wasted_wall_ms, wasted_usd, recoverable: bool }
  value_schema : json-schema | null     # kind = supply_value
  dedupe_key   : string
}

HumanResolution {
  decision_id, resolver, choice: option_id, value?, note?,
  resolved_at, via: <sink|connector name> | :default_expired
}
```

### 3.1 The 60-second rule

A card MUST be resolvable in under a minute by someone who has not been
watching. Therefore:

- `context` is **folded state** (`04 §3.1`) – what was attempted, what is
  blocked, evidence by reference. A card MUST NOT carry a transcript, and MUST
  NOT carry `rationale` (audit-only, `01 §8.1`).
- Composition of `context` and `options` MAY use a model (this is the
  supervisor's job, `16 §5`); the *policy* of whether to ask MUST NOT (§2).
- `options` SHOULD be 2–3 and MUST each state a `consequence`. Exactly one MAY
  be `recommended`.
- `cost_of_wrong` is what makes a card skimmable: it tells the human whether to
  think or to tap.

### 3.2 Anti-patterns (MUST NOT)

- A card that asks the human to read the run and decide what to do. That is not
  a decision, it is a handoff of the supervisor's job.
- A card raised for an `internal` act.
- A free-text card where a `choose` would do.
- A card whose resolution the substrate cannot execute without another card.

---

## 4. Silence – defaults, TTL, and parking

Silence MUST advance the run or explicitly hold it. It MUST never be undefined.

```
reversibility = internal | recoverable   →  default REQUIRED, ttl REQUIRED  (fail-safe)
reversibility = external                 →  default FORBIDDEN, no ttl       (fail-secure)
```

This is the fail-safe / fail-secure split from process control, and inverting it
is the single most dangerous defect available in this design: a default on an
`external` act means **silence authorizes an irreversible action.** The schema
forbids it structurally (`default` null ⇒ `ttl_ms` null and vice versa), and the
substrate MUST reject a card that violates it rather than downgrade it.

On expiry the substrate emits `decision_defaulted` and proceeds as if the
default had been chosen by a resolver named `:default_expired`. The distinction
is preserved on the log forever, because it feeds `decision_precision` (`16 §6`).

### 4.1 Two parking tiers

| Tier | When | Mechanism | Cost held |
|---|---|---|---|
| **hold** | expected answer within minutes | `SIGSTOP` the process tree; sandbox stays warm | full sandbox |
| **park** | `external`, or hold exceeded `park_after` (default 10 min) | checkpoint (`16 §3`) → snapshot → **release compute** | storage only |

Without the second tier "unattended overnight" costs a night of idle sandboxes,
which is the difference between a feature and a bill. Rehydration on resolution
is the ordinary `04 §7` resume path – no new mechanism.

---

## 5. Transport – a sink out, a connector in

A decision needs no new plane. It is exactly the `10 §5.4` round-trip.

### 5.1 The `decide` sink (`10 §5.2`)

Renders a card to a destination that can express a structured choice: a push
app, a chat with inline buttons (Telegram/Signal/Slack), email with resolution
links, the web console, the CLI. `render/2` reads folded run state only, and
applies the viewer's cut (`08 §5`) like every other render.

### 5.2 The `decision` connector (`10 §3.1`)

Receives the resolution, authenticates the resolver against the tenant, dedupes
on `decision_id`, and emits `approval_resolved`. `reply_ref` is the delivered
card's message id, exactly as a chat connector carries a thread.

- A resolution from an unauthenticated or out-of-tenant resolver MUST be dropped
  with a `decision_rejected` Effect, never applied.
- Resolution is **idempotent** on `decision_id`; the first valid one wins and
  later ones are recorded, not applied.
- The transport is untrusted. The card's authority lives in the log, not in the
  message; a forged button press cannot widen `blast_radius` because the
  substrate re-checks the act against the run's grants before executing.

### 5.3 Batching, dedupe, and the floor

- `dedupe_key` collapses identical cards across parallel workers into **one**
  card. Resolving it resolves every member.
- Cards below the tenant's `escalation_floor` accumulate into a **digest**
  delivered on a schedule – a list to read, not a push to answer. `recoverable`
  acts land here by default.
- Pushes are rate-limited per tenant; excess degrades to digest, never to drop.
- Effects (`01 §8.2` open list): `decision_delivered`, `decision_defaulted`,
  `decision_rejected`. `approval_requested` / `approval_resolved` keep their
  canonical names and carry the structures above.

---

## 6. One mechanism, two policies

The plane is shared; the policy differs by product (`02 §1`). This table is the
contract that keeps Studio's convenience out of Market's trust boundary.

| | **Studio** (operator) | **Market** (commerce, `08`) |
|---|---|---|
| Resolver | the operator (tenant-authenticated) | the **buyer** for acts on their own data; the **seller** for policy set ahead of the run |
| Transport | push / chat / CLI / console | the entrypoint's `reply_ref` + the commercial-cut watch page (`11 §4`) |
| Defaults | permitted for `internal` / `recoverable` | **never permitted** – a stranger's silence MUST NOT authorize spend or egress |
| Frequency target | rare, by exception (`16 §6`) | **zero on the happy path** (`02 §6`) |
| A card on the happy path | normal | a **defect** in the capability, not a feature – the seller must resolve it in policy before publishing |
| Cut | full dev cut | commercial cut: no `rationale`, no inner trace, destinations only (`08 §5`) |

That last row is how `02 §6`'s "no human in the loop on the happy path" survives
this doc: Market runs may raise decisions, but only on the exception path, and a
capability that routinely raises them fails its own launch criteria.

---

## 7. Open questions

- **Resolver identity off-console.** A button press in a chat client must map to
  a sigid-asserted principal (`01 §0`). Binding a chat account to a tenant
  identity is an unsolved seam; until it is, `external` resolutions SHOULD be
  restricted to the console and CLI.
- **`escalation_floor` calibration.** The floor should be *learned* from
  `decision_precision` (`16 §6`) rather than configured. Mechanism undecided.
- **Amend-brief semantics.** `kind: amend_brief` edits the pipeline intent
  (`04 §8`) mid-run; whether the amendment applies from the next segment or
  forces a rewind to the segment where the brief last changed is undecided.
  Default until settled: next segment.

## Cross-references

- Decision/Effect schema, decision card contract: `01 §8.1`, `§8.2`, `§8.9`.
- Broker `requires_approval` parking: `05 §5.3` – a card is how it is asked.
- Sinks, connectors, round-trip: `10 §3`, `§5`.
- Escalation signals that raise cards: `04 §4.3`, `16 §2`, `16 §4`.
- `:unknown` modal state and rejected steers: `14 §3.2`, `§3.3`.
- Cuts and personas: `08 §5`, `11 §3`.
