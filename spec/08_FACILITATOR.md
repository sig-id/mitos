# 08 — Gentix Facilitator

Status: draft v0.2 · Date: 2026-06-18

This is the business/monetization spec. Mitos (the substrate, `01_ARCHITECTURE`)
is the engine; **Gentix is the hosted, multi-tenant facilitator that runs on it**
and is the revenue path for ~2 years. It fixes how a packaged capability becomes a
metered paid endpoint, how charges are derived off the event log (01 §8.2), how
isolation, key custody, and settlement work, and what the MVP must do to clear the
success gate. It defines no new contracts — it composes `01`'s. Where this and
`01` disagree, `01` wins.

RFC 2119 keywords (MUST / SHOULD / MAY) are normative.

---

## 1. Thesis — the capability is the monetizable unit

`01 §1` makes the capability `(data + skill + tools)` the atom of *execution*.
Gentix makes that same atom — and pipelines of it — the atom of *monetization*.
The seller's recipe (skill.md, tool code, prompts, dataset) stays **server-side
and never ships**; only the run's **outputs + proof** ship to the buyer.

> **Pickaxe, but built on coding-agent harnesses in sandboxes, not raw model-API
> calls.** Pickaxe resells single prompt→completion calls as "apps". Gentix
> resells whole **jobs** executed by a real harness (Claude Code, Codex, Gemini
> CLI; `01 §4`) inside a T2 sandbox, and delivers a completed artifact set with an
> evidence/proof page — not a chat response.

| | Pickaxe-class | **Gentix** |
|---|---|---|
| Unit sold | one model call dressed as an app | a **capability/pipeline run** (`01 §1`) |
| Execution | provider API call | full harness in a sandbox (`01 §3`, `05`) |
| Recipe exposure | prompt often leaks | recipe stays server-side; outputs+proof ship |
| Deliverable | chat text | harvested diff + RESULT + artifacts (`05`, 01 §8.2) |
| Trust story | "trust the prompt" | event-log proof page, replayable (01 §8.2) |
| Provider cost | platform's keys | **seller's own keys**, brokered (01 §8.5) |

Two revenue lines, in priority order:

1. **Hosted take — primary (~2 yr).** 10–15% on hosted GMV (GMV = buyer payments
   for runs Gentix executes). This is the focus of every section below.
2. **Protocol take — secondary.** 3–5% on runs settled through the open
   metering/settlement protocol but executed elsewhere. Depends on the open
   engine being adopted (§10); not a near-term number.

---

## 2. Metering — the event log *is* the billing ledger

`01 §8.2` already declares the event log to be "run state, debug trace, replay
source, audit record, **and billing ledger (`08`)**." Gentix adds **no parallel
meter**: every billable signal is an Effect event the substrate already emits, and
metering is a deterministic fold over the log — exactly like state (`01 §4`:
`state(t) = fold(events[0..t])`).

### 2.1 Effect events → billable / audit signals

| Effect event (`01 §8.2`) | Billable / audit signal | Used for |
|---|---|---|
| `capability_started` | compute clock starts | compute metering, proof timeline |
| `tool_brokered` | one brokered tool/credential use | metered unit, audit |
| `egress_allowed` | one outbound crossing (+`ref.dest`) | metered unit, proof-of-work, cost |
| `egress_denied` | blocked attempt | abuse signal, audit (never billed) |
| `token_usage`† | provider tokens in/out + usd | **seller's** provider cost (own key), transparency |
| `budget_charged` | committed usd/token/wall vs budget | the **authoritative money line** |
| `capability_finished` | compute clock stops; RESULT ready | compute total, GMV trigger, proof |
| `artifact_registered` | one deliverable artifact | metered unit, delivery manifest |

† `token_usage` is normalized from the harness adapter stream (`01 §8.6`) and
folded into `budget_charged`. `budget_charged` is canonical; never double-count
raw `token_usage` against the buyer.

### 2.2 How a charge is derived

```
per run R, over events[0..end]:

  compute_ms    = ts(capability_finished) − ts(capability_started)
  provider_cost = Σ budget_charged.usd            # on SELLER's OWN key (01 §8.5) — NOT settled via Gentix
  egress_units  = count(egress_allowed)
  tool_units    = count(tool_brokered)
  artifacts     = count(artifact_registered)

  buyer_charge  = price(capability, params, run)  # seller-declared at publish (§6)
                  ├ flat:    fixed_usd
                  └ metered: base + Σ unit_price[k] × units[k]   (k ∈ {token,egress,tool,artifact,ms})

  compute_cost  = compute_ms × rate(trust=T2)     # Gentix infra cost (µVM, 04)
  platform_take = max(take_floor, take_rate × buyer_charge)     # take_rate ∈ [0.10, 0.15]
  seller_net    = buyer_charge − platform_take − compute_surcharge

GMV  = Σ buyer_charge over settled runs
take = Σ platform_take
```

Key economic stances (opinionated, MUST hold):

- **Provider tokens are the seller's cost, on the seller's own key.** Gentix
  brokers the key (01 §8.5), records `budget_charged` for transparency, never
  moves that money — staying out of reseller margins and provider ToS.
- **Compute is Gentix's cost**, recovered from the take or an explicit
  `compute_surcharge`; a `take_floor` MUST exist so a cheap job can't run net-
  negative on µVM cost.
- **Every charge line is reconstructible from the log.** A charge with no backing
  `budget_charged`/`capability_finished` event is invalid by construction; the
  same fold feeds billing and the proof page (§7) — one source, no drift.

---

## 3. Multi-tenancy

Tenancy model: **Org → Users → {Capabilities, Keys, Payout}**. A *tenant* is the
billing + isolation boundary. Buyer and seller are always distinct tenants; a run
spans both (seller's recipe, buyer's data + payment).

```
SELLER tenant                         BUYER tenant
  capability image (SKILL+TOOLS)         params + input workspace (their data)
  RO data snapshot                       USDC payment (x402, §5)
  provider keys (vault, §4)              ─────────────────────────────┐
        │                                                             │
        ▼   run R executes in BUYER's isolation context              ▼
  ┌──────────────────────────────────────────────────────────────────┐
  │ T2: remote µVM (E2B/Daytona) · broker-only egress, deny-all-else  │  01 §6, 04
  │   inject (hermetic): image + RO data + buyer workspace            │  01 §8.5
  │   per-tenant memory namespace (07) — hermetic by default          │  00 princ. 8
  └──────────────────────────────────────────────────────────────────┘
```

- **Isolation MUST be T2** for every marketplace run (`01 §6`): untrusted seller
  capability on buyer data ⇒ remote µVM, broker-only egress, RO data snapshot.
  T2 is non-negotiable in the marketplace; sellers MAY NOT downgrade trust.
- **Buyer data is RO-injected** into the workspace and **never persists into the
  seller's cross-run memory** — hermetic by default (`01 §8`, princ. 8; `07`).
  Memory is per-tenant namespaced (`07`); a seller's capability cannot read across
  buyer tenants.
- **Per-tenant budgets** (`01 §8.1` `budget`) cap blast radius: a buyer's prepaid
  amount is the run's hard ceiling; the orchestrator MUST `abort` on exhaustion.

---

## 4. Key custody — seller BYO keys, broker-only injection

Sellers bring their own provider keys (Anthropic, OpenAI, image APIs, their own
SaaS). This is enforced through the **broker as the single key-custody point**
(`01 §8.5`). The constitutional invariant — *no secrets enter the sandbox* — is
the whole mechanism.

```
seller registers key ─► per-seller broker vault (control plane, BEAM; 01 §7)
                              │  scope: (seller_id, dest) → credential_ref
   run R, sandbox egress ─────┤  egress is deny-all-except-broker (01 §8.5)
        capability hits dest ─► BROKER  ── matches egress_policy[dest] ──►
                              │  attaches REAL credential per (seller, dest)
                              │  emits egress_allowed | egress_denied (01 §8.2)
                              ▼
                          destination (provider / SaaS)
```

| Property | Rule |
|---|---|
| Where keys live | broker vault only, control-plane side (`01 §7`); never in `env` (01 §8.5 inbound) |
| Injection point | at the egress crossing, per `egress_policy` `{dest, credential, requires_approval}` (01 §8.3) |
| Scoping | `(seller_id, destination)`; a key for dest A is never attachable to dest B |
| Visibility | the harness/capability sees a request *succeed or fail*, never the secret |
| Lease | run-scoped; granted at `capability_started`, **revoked at `capability_finished`/`abort`** |
| Revocation | lease teardown on run end; cached material rotated out; long-lived keys re-fetched per run |
| Audit | every attach/withhold is an Effect event (`egress_allowed`/`egress_denied`) on the log |

This is why "the recipe never ships" and "the buyer can't exfiltrate the seller's
keys" are the *same* guarantee: both are the sandbox boundary (`01 §3`, princ. 3),
not a policy the harness is trusted to honor.

---

## 5. Settlement — USDC / x402

Settlement uses **x402** (HTTP 402 Payment Required + stablecoin) so a stranger
with a wallet — or an agent — can pay an endpoint with no account. Flow:

```
buyer ──POST /capabilities/{id}/runs {params}──►  Gentix endpoint (§6)
      ◄── 402 Payment Required {amount_usdc, pay_to=escrow, quote_id} ──
buyer ──repeat + X-PAYMENT (USDC transfer proof)──►  endpoint
      verify settlement/escrow ─► admit run ─► execute in T2 (§3) ─►
      harvest (05) ─► deliver outputs + proof page (§7) ─►
      split: platform_take → Gentix · seller_net → seller payout addr
```

| On-chain (USDC) | Off-chain (Gentix control plane) |
|---|---|
| buyer → escrow payment | the quote (price computation, §2.2) |
| escrow → seller payout | the run, recipe, keys, outputs |
| escrow → Gentix take | metering fold over the event log (§2) |
| (optional) anchor of log-hash for proof | take-rate split math, payout batching |

- **Quote-before-run.** Flat price is quoted up front. For metered pricing
  (§2.2) the 402 quote MUST be an escrow *ceiling*; the final charge is the log-
  derived amount ≤ ceiling, remainder refunded from escrow.
- **No buyer payment ⇒ no run.** Admission gates on verified payment/escrow;
  refusal returns 402 and emits nothing billable.
- **Failure refunds.** If the run `abort`s before the declared RESULT
  (`01 §8.3`), escrow refunds the buyer minus `compute_surcharge` only — no
  `platform_take` on a non-delivery. The log decides delivery, not the seller.
- **Settlement is a fold + a transfer.** On `capability_finished` the metering
  fold (§2.2) yields the split; payouts MAY be batched off-chain, flushed on-chain
  to amortize gas.

---

## 6. Seller onboarding — recipe → metered endpoint

```
1 author      skill.md + tools + data  ──build──►  OCI image (SKILL+TOOLS) + RO data snapshot   (01 §5, §8.3; 02)
2 manifest     trust_tier=T2 · resources · egress_policy[{dest,credential,requires_approval}]
               · result{schema, evidence[]}                                                     (01 §8.3; 02)
3 keys         register provider keys → broker vault, scoped (seller_id, dest)                   (§4; 01 §8.5)
4 price+payout declare pricing (flat | metered, §2.2) + USDC payout address                      (§5)
5 validate     policy-compile (05) + dry-run in T2 + result-schema check                         (04; 03)
6 publish      Gentix-compliant metered endpoint: /capabilities/{id}/runs (x402, §5)             (this §; 05 dispatch)
```

- Onboarding reuses the capability authoring + manifest path (`03`) and the
  policy compiler (`05`); Gentix only *adds* pricing, payout, key-vault binding,
  and endpoint publication. It MUST NOT fork the manifest schema (`01 §8.3`).
- `trust_tier` is **forced to T2** at publish for any marketplace capability,
  overriding a seller-declared lower tier.
- A **Gentix-compliant endpoint** = the run-trigger surface (`06` dispatch) +
  x402 admission (§5) + the result/proof delivery contract (§7). The harness is
  never named to the buyer (`01 §7`, princ. 7).

---

## 7. Buyer delivery + proof — "completed jobs with evidence, not chat"

Delivery is the harvest contract (`01 §8.2`/`05`): `harvest(session) → {diff,
result, artifacts}`. On top of it, Gentix renders a **proof/evidence page** from
the run's event-log excerpt — *this page is the product differentiator.*

```
DELIVERY = { workspace diff, RESULT (01 §8.3 result.schema), registered artifacts }   ← 04 harvest
PROOF    = render( events[capability_started .. capability_finished] ):
             timeline · tools_brokered · egress crossings (dest only) · token totals
             · budget_charged · artifacts manifest · run wall-time · log-hash anchor
```

| Proof page SHOWS (what happened) | Proof page HIDES (the recipe — never ships) |
|---|---|
| egress destinations, counts, timing | skill.md / prompts / `rationale` text (01 §8.1) |
| `budget_charged` totals, artifact list | tool source, dataset contents |
| RESULT + the delivered artifacts (`05`) | which harness ran (`01 §7`) |
| event-log hash (replay/audit anchor) | seller keys (never on the log; §4) |

- Proof is generated from Effect events only; Decision-event `rationale`
  (`01 §8.1`, "AUDIT ONLY") is **redacted** from buyer-facing proof — it leaks
  recipe. Full audit-grade proof goes to the seller and dispute resolution only.
- The deliverable is the **artifact set + proof**, not a transcript — literally
  the harvest output plus a fold over the same billing ledger that computed the
  charge (§2). One log, three faces: bill, audit, proof.

---

## 8. The wedge + the success gate

**Design partner #1**: a designer already **paid by strangers** but fulfilling
every order **manually**. She has product-market fit and a payment relationship;
she lacks machine fulfillment. That gap is exactly Gentix.

**Success gate (single, binary):** *one stranger-paid, machine-fulfilled
transaction, with proof.* Not a marketplace, not throughput — one end-to-end
dollar that the machine, not the human, earned.

MVP must do exactly this end-to-end, and no more:

| MVP must-have | Spec owner |
|---|---|
| package her workflow as **one** T2 capability (skill+tools, optional RO brand data) | `03`, `01 §5/§8.3` |
| publish **one** Gentix endpoint, **flat** price | this §6; `06` |
| accept **one** x402/USDC payment from a stranger | this §5 |
| run in T2 with **her own** provider key brokered | this §4; `05`, `01 §8.5` |
| harvest → deliver artifacts + proof page | this §7; `05` |
| settle: take split + seller payout | this §5 |

| Explicitly deferred past the gate | Why |
|---|---|
| metered pricing, multi-capability pipelines | flat + single capability proves the loop |
| marketplace discovery / multi-seller | one seller, one buyer suffices |
| protocol take (3–5%) | depends on open-engine adoption (§10) |
| compute-surcharge tuning, payout batching | optimize after the dollar exists |

The gate is deliberately ruthless: if one real stranger can't pay and receive a
machine-made artifact with proof, nothing downstream matters.

---

## 9. Backlog / gaps → owning spec

The pivot backlog, each item mapped to the spec/section that owns the mechanism
(this doc owns the *business* wiring; the substrate specs own the mechanism):

| Gap | Mechanism owner | Business wiring (this doc) |
|---|---|---|
| multi-tenancy (orgs/users/tenants) | isolation `05` (T2, `01 §6`); memory `07` | §3 |
| seller onboarding | manifest/authoring `03`; policy `05` | §6 |
| key custody | broker `05` / `01 §8.5` | §4 |
| metering / billing | event log `01 §8.2` | §2 |
| settlement (x402 / USDC) | — (new external rail) | §5 |
| Gentix-compliant public endpoint | dispatch/triggers `06` | §6, §5 |
| buyer delivery + proof | harvest `05`; log `01 §8.2` | §7 |
| compute-cost metering | `capability_finished` `01 §8.2` | §2.2 |
| build sequencing | `09_IMPLEMENTATION_PLAN` | §8 (MVP cut) |

Nothing here requires a new canonical contract. x402/USDC settlement is the only
genuinely external dependency; everything else is a fold, a policy, or a
publication surface over contracts `01` already fixes.

---

## 10. Open / closed split

The engine leans **open**; the hosted facilitator leans **closed**. The split is
chosen so the two take-rates reinforce each other.

| Open-source (the engine — drives adoption) | Closed (hosted Gentix — the moat) |
|---|---|
| event log + Decision/Effect contracts (`01 §8.1/§8.2`) | Gentix endpoint + x402 admission (§5) |
| capability manifest format (`01 §8.3`; `03`) | metering→billing→settlement service (§2, §5) |
| `SandboxProvider` + harness adapter (`01 §8.4/§8.6`) | per-seller key vault + take engine (§4, §2) |
| broker *interface* (`01 §8.5`) | hosted broker + provider key custody |
| loop-manager FSM, orchestrator contract (`04`) | proof-page renderer + dispute/audit grade (§7) |
| local CLI / single-machine mode (`01 §1.6`) | multi-tenant control plane + hosted T2 pool (§3) |

Rationale: the **protocol take (3–5%)** is only collectible if the open engine and
its contracts are adopted widely enough to settle through Gentix — so the engine
must be forkable. The **hosted take (10–15%)** is collectible because the closed
facilitator is the *easiest place to actually run* a T2 capability with brokered
keys, metered billing, and one-click USDC settlement. Open the standard, close the
operation. For ~2 years the hosted take pays the bills; the open engine is the
option on the protocol take later.
