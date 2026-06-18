# 02 — Product

Status: draft v0.2 · Date: 2026-06-18

Scope: the product thesis, who it is for, the atomic unit, the business it funds,
the wedge it must win, and the smallest build that wins it. Vocabulary and
contracts are fixed by `01_ARCHITECTURE` and are not redefined here. Monetization
mechanics live in `08_FACILITATOR`; this doc only frames *why* they exist.

Almost everything below is **TARGET**. What is **IMPLEMENTED** today is a Zig
engine (local pty/stdio transports, terminal codec, a harness/CLI detector) and a
BEAM runtime skeleton — two half-built control planes that this redesign collapses
(`09_IMPLEMENTATION_PLAN`). Treat IMPLEMENTED claims as the floor, not the plan.

---

## 1. Thesis

> **Mitos is a cloud-first, multi-tenant substrate that runs packaged
> capabilities through agent-driven pipelines, where an agent decides policy and
> deterministic code records every decision and enforces every limit.**
> — `01_ARCHITECTURE §0`

Mitos is the **substrate / OS** for selling *completed work*, not the seller's
prompt and not the model. It owns everything outside the harness: the run
lifecycle, the event log, the sandbox boundary, the egress broker, metering, and
multi-tenancy. The harness (Claude Code / Codex / Gemini CLI) does the cognitive
work; mitos makes that work **bounded, isolated, replayable, and billable**.

### Positioning

> **Pickaxe, but built on coding-agent harnesses running in sandboxes, not raw
> model-API calls.** It sells *completed jobs with evidence/proof pages*, not chat
> responses.

Pickaxe (and the single-prompt-wrapper class) sells a templated call to a model
API. Mitos sells the output of a full coding-agent *harness* — file edits, tool
runs, multi-step pipelines — executed in an isolated sandbox, harvested as a diff
+ `RESULT` + artifacts (`01 §8.5`), and presented as a **proof page**: the
evidence bundle and a replayable trace from the event log. The buyer pays for a
finished job they can inspect, not a turn of conversation.

---

## 2. What mitos IS vs IS NOT

| Mitos IS | Mitos IS NOT |
|---|---|
| a substrate/OS that runs packaged **capabilities** (`01 §5`) | a chatbot or assistant UI |
| a host for **agent-driven pipelines** the orchestrator enforces (`04`) | a single-prompt / single-API-call wrapper (Pickaxe-class) |
| **harness-agnostic** — wraps Claude Code, Codex, Gemini CLI, … (`01 §1.4`) | tied to one model vendor or one harness |
| an **agentic** control plane: policy is reasoned, record is deterministic | a fixed/authored pipeline-runner or hard-coded FSM workflow tool |
| the **authority boundary** at the sandbox edge — inject / broker / harvest (`05`) | a trust model that relies on the agent or a harness protocol |
| **cloud-first, multi-tenant**, locally runnable (`01 §1.6`) | a single-tenant local-only dev toy |
| a seller of **finished jobs with proof**, metered off the event log (`08`) | a seller of chat turns or raw tokens |

The "not a fixed pipeline tool" line is the hard one and the reason for the
redesign: a pipeline is **enforced at runtime by an orchestrator agent**, not
declared as a state machine. chief-wiggum's pipeline is a fixed bash kanban loop
(`/media/data/proj/chief-wiggum/README.md`, `lib/`) that compiles tasks → PRs
through pre-authored stages; mitos keeps the "ship a finished artifact with
metrics" outcome but replaces the authored stage machine with an orchestrator that
emits Decision events (`01 §8.1`) and a deterministic loop-manager (`04`).

---

## 3. Target users

### PRIMARY — hosted multi-tenant orgs (via the Gentix facilitator)

Two roles inside one hosted service:

- **Capability sellers** — own a prompt/skill, a pipeline, and/or a dataset.
  They package it as a capability (`03_CAPABILITY`), bring **their own provider
  keys**, set a price, and publish a metered, paid endpoint. The recipe stays
  server-side; only outputs ship (`08_FACILITATOR`).
- **Capability buyers** — pay per job, send inputs, and receive a **completed
  job + proof page**. They never see the recipe.

This is the revenue surface and the reason mitos is cloud-first and multi-tenant
rather than a local runner. Multi-tenancy, key custody, metering, and settlement
are owned by `08`.

### SECONDARY — a single developer running the whole stack on one machine

The same backend operations (`01 §10.11`), run locally: author a capability, run a
pipeline against your own repo, watch the event log. Trust tier `T1` (`01 §6`),
local broker, Docker/Nucleus sandbox. This is a **supported** mode and the daily
authoring/debug loop — but it is not the business. Cloud is the product; local is
the workbench.

```
            ┌──────────── Gentix facilitator (08) ───────────┐
  seller ──►│ package capability · own keys · price · publish │
            │                  M I T O S                      │──► proof page
  buyer  ──►│ pay · submit input · receive completed job      │     + receipt
            └─────────────────────────────────────────────────┘
  dev ────► same backend ops, one machine, T1, local broker   (secondary)
```

---

## 4. The atom: capability → pipeline → money

The **capability** is the single atomic unit across all three concerns —
execution, composition, and monetization (`01 §1.1`):

| Concern | Unit | Owner doc |
|---|---|---|
| execution | capability = `(data + skill + tools)` OCI image under an injected harness, in a sandbox | `03`, `05` |
| composition | **pipeline** = a markdown composition of capabilities, enforced by the orchestrator | `04` |
| monetization | the **same capability** is what a Gentix seller meters and prices | `08` |

One unit, not three, is the load-bearing product decision. It means a seller
prices the exact thing the substrate executes, and metering reads the **event
log** directly (`01 §8.2`, `08`) — no separate billing model to reconcile against
execution. A pipeline is just a composition; it is priced and metered as the sum
of its capability crossings on the log. Mechanics: see `08_FACILITATOR`. Do not
duplicate them here.

---

## 5. Business framing (the 2-year revenue path)

The revenue path for ~2 years is the **Gentix facilitator** — context you need to
read this spec set correctly, detailed in `08`:

| Lever | Setting |
|---|---|
| What it sells | a hosted service that packages a seller's prompts / pipelines / datasets into **metered, paid endpoints** |
| Recipe vs output | recipe stays **server-side**; only outputs ship |
| Primary take | **~10–15%** on hosted GMV |
| Secondary take | a protocol take of **~3–5%** (kept secondary for ~2 years) |
| Provider keys | sellers **bring their own** provider keys (mitos custodies, never the recipe owner's model bill) |
| Settlement | **USDC / x402** |
| Engine vs service | the **engine leans open-source**; the **hosted service leans closed** |

Why this shape: the take-rate on completed, proven jobs is the wedge that funds the
substrate. The open engine drives adoption and authoring; the closed hosted service
(multi-tenancy, key custody, metering, settlement, proof delivery) is where the
margin is. Mitos's standalone agent-OS positioning is unchanged on paper, but the
facilitator is the money.

---

## 6. The wedge and the success gate

**Design partner #1:** a designer who is **already paid by strangers** for a
repeatable creative job but **fulfills it manually**. The demand is proven; the
fulfillment is human. Mitos's job is to move fulfillment behind a capability
without losing the buyer's trust — which is why the output ships as a **proof
page**, not a chat reply.

**Success gate (single, binary):**

> ONE stranger-paid, machine-fulfilled transaction, with proof.

Decomposed, every clause is a hard requirement:

| Clause | Means | Owned by |
|---|---|---|
| **stranger** | a buyer with no prior relationship; public metered endpoint | `08` |
| **paid** | real settlement (USDC/x402) before/at delivery | `08` |
| **machine-fulfilled** | the capability ran in a sandbox; no human in the loop on the happy path | `03`, `04`, `05` |
| **with proof** | evidence bundle + replayable trace the buyer can inspect | `01 §8.2–8.5`, `04` |

Everything in the MVP exists to make that one sentence true once. Nothing that is
not on the path to it is in scope.

---

## 7. MVP scope

The smallest end-to-end path that reaches the success gate — one capability, one
trivial pipeline, one buyer, one proof:

```
capability ──► pipeline ──► sandbox ──► broker ──► harvest ──► proof
 (03)          (04)         (05 T2)     (05)        (05 §8.5)   (04/08)
  one          orchestrator  remote      egress      diff +      evidence +
  seller       enforces a    µVM, RO     cred per    RESULT +    replayable
  capability   1–2 cap       snapshot    egress_     artifacts   trace +
  +own keys    composition   of input    policy                  receipt
```

| # | MVP must do | Spec |
|---|---|---|
| 1 | author + publish **one** capability (skill+tools+optional data), seller keys held by broker | `03`, `08` |
| 2 | a buyer hits a **public metered endpoint**, pays (USDC/x402), submits input | `08` |
| 3 | orchestrator runs a **1–2 capability** pipeline; emits Decision/Effect events to the log | `04` |
| 4 | run in a **T2 remote µVM** sandbox; input as RO snapshot; **no secrets in the sandbox** | `05`, `01 §6` |
| 5 | **broker** is the only egress; attaches seller credential per `egress_policy`; logs every crossing | `05`, `01 §8.5` |
| 6 | **harvest** diff + `RESULT` + artifacts; build the **proof page** from evidence + trace | `01 §8.5`, `04` |
| 7 | **meter** off the event log; settle; take the facilitator cut | `08` |

### Non-goals for MVP (explicitly out)

- Marketplace, discovery, seller dashboards, ratings — one design partner, no market.
- Multi-capability pipeline depth, branching, or sophisticated orchestration — 1–2 steps suffice.
- Memory (`07`), self-improvement / verify-and-distill loops (`04`) beyond what proof needs.
- Harness-switching breadth — **one** harness wired end-to-end is enough; the agnostic seam (`01 §8.6`) stays a seam.
- Rich scheduling/services (`06`) — trigger is the buyer's paid request; no cron/webhook fan-out.
- Protocol-take settlement (`08`) and the second take lever — primary take only.
- Polished local-dev DX — secondary mode must *work*, not delight.

The MVP proves the sentence in §6 **once**, not at scale.

---

## 8. Key product risks and how the architecture mitigates them

| # | Risk | Why it threatens the product | Mitigation (owning spec) |
|---|---|---|---|
| R1 | **Harness fragility** — coding-agent CLIs change output formats, break, hang | screen-scraping breaks fulfillment silently; buyer gets a bad job | normalized harness adapter contract with structured events; PTY is a *fallback codec*, not the path (`01 §8.6`); harness-switchable mid-step; loop-manager FSM detects stuck/escalate (`04`) |
| R2 | **Untrusted-workload isolation** — seller capability runs on buyer data; both are untrusted to mitos | a leak or escape ends the business on transaction #1 | trust tier **T2** ⇒ remote µVM (E2B/Daytona), **deny-all egress except broker**, RO snapshot of buyer data, no secrets in env (`01 §6`, `05`) |
| R3 | **Seller-key custody** — sellers bring provider keys; recipe must stay server-side | a key leak or recipe exfiltration is a breach and a trust collapse | secrets live **only at the broker**; broker attaches/withholds per `egress_policy` + destination and logs each crossing; recipe never enters the buyer-visible surface (`01 §8.5`, `05`, `08`) |
| R4 | **Cost of LLM verification** — proving a job is "done" with an LLM judge is expensive and can exceed the margin | verification cost eats the 10–15% take | verify **cheaply and tiered**, reuse verification as the self-improvement signal (`01 §10.10`, `04`); for MVP, proof = evidence + replayable trace, LLM-judge only where a deterministic check can't decide |

Cross-cutting: the **event log is the source of truth** (`01 §8.2`) — it is
simultaneously run state, audit record, the proof page's backing data, and the
billing ledger. Every risk above is observable on it after the fact, which is the
difference between "a job went wrong" and "a job went wrong and we cannot say
how."

---

## 9. Definition of done (MVP)

MVP is done when, with no human on the happy path:

1. a stranger pays a public metered endpoint for design partner #1's capability;
2. the capability runs in a T2 sandbox, egress only through the broker;
3. the buyer receives the output + a **proof page** (evidence + replayable trace);
4. the event log yields the meter, the settlement, and the facilitator take;
5. the seller's keys and recipe never left the server-side trust boundary.

One such transaction is the gate. Scale, marketplace, memory, and pipeline depth
are post-gate and are sequenced in `09_IMPLEMENTATION_PLAN`.
