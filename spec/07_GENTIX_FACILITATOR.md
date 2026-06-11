# Gentix Facilitator — Hosted Capability Service on Mitos

Status: decided v1.0 (business strategy); product draft v0.2
Date: 2026-06-11

v0.2 (2026-06-11): positioning sharpened. The facilitator is **"Pickaxe for
agent harnesses"** — it packages and sells completed agent *jobs*, not
prompt-wrapped chat. New: harness execution model (§4), key-custody compliance
constraint (§5), async delivery model (§6), competitive frame (§10). Gap
analysis (§13) updated accordingly.

This document describes the **facilitator**: a hosted commercial service built
on the mitos runtime that turns a seller's process into a paid, metered
endpoint on the Gentix protocol. It does not change mitos's core positioning
(AI-native agent operating system, `01_PRODUCT.md`); the facilitator is the
first hosted *application* of that runtime. Business context lives in
`gentix/spec/product/00_STRATEGY.md`, which this document pairs with.

---

## 1. What It Is

The facilitator takes what a seller actually has — prompts, a structured
process, a pipeline, a private dataset, agent skills — and produces what the
Gentix protocol requires: a **spec-compliant, hosted, metered endpoint** with
a shareable payment link.

The pitch to the seller: *from "it runs on my machine" to "strangers from my
tweet are paying per use" in minutes, not weeks.* Time-to-first-paid-call is
the product's defining metric.

Positioning in one line:

> **Pickaxe sells chatbots; the facilitator sells workers.**

A buyer does not get a chat session with a prompt wrapper. They pay a link,
an agent harness runs the seller's process in a sandbox, and they get back
**completed work** — artifacts, outputs, and a proof page.

## 2. Harnesses, Not Raw APIs

The defining architectural choice: a capability is **not** a prompt chain over
raw model APIs (the Pickaxe / Wordware / Glif shape). A capability is a
seller's process — skill files, process definitions, private data —
**executed by a full agent harness** (Claude Code, Codex CLI, Gemini CLI, or
any harness mitos has a transport for) inside a mitos sandbox.

Why this matters:

- **Capability ceiling.** A harness embodies the provider's agentic R&D —
  multi-step tool use, file operations, test loops, skills, MCP. The ceiling
  on what a seller can package is "a completed job," not "a good
  conversation." No prompt-chain builder can reach this.
- **Durability.** Frontier releases absorb prompt-level differentiation on a
  ~6-month cadence (`gentix/spec/product/00_STRATEGY.md` §10). A process +
  skills + private-data capability executed by a harness rides those releases
  instead of being absorbed by them: the harness gets better, the seller's
  capability gets better.
- **The runtime earns its place.** Autonomous harness execution of an
  untrusted seller payload against a paying stranger's input *requires* what
  mitos already is: Nucleus sandboxing (mandatory, not optional), allow/ask/
  block policy (the seller's safety constraints on their own packaged agent),
  and the evidence pipeline (the buyer's proof page). API-wrapper builders
  would have to build all of this to enter; it is the moat candidate.

What mitos's runtime features mean in facilitator terms:

| Runtime feature | Facilitator meaning |
|---|---|
| Sandbox (Nucleus/gVisor) | Untrusted seller payload × stranger input, safely multi-tenant |
| Policy (allow/ask/block) | Seller-defined limits on what their packaged agent may do per call |
| Run evidence (diffs, transcripts, approvals) | The buyer-facing proof page; substrate for pay-if-good verification |
| Transport abstraction | Any harness becomes packageable supply |

## 3. Why It Exists

- The Gentix protocol's seller-side interface is a compliant endpoint, and
  hosting/execution is explicitly out of protocol scope.
- The wedge customer (designers with prompt processes, skill authors,
  interface-light app builders) has **no infrastructure** and cannot produce
  that endpoint. They enter the market only through a facilitator.
- Their assets are usually **text, which leaks if delivered**. Monetization
  requires the asset to stay server-side: outputs ship, the recipe never
  does. That means someone must host the recipe. That someone is the
  facilitator.
- Validated: design partner #1 (designer archetype) is already being paid by
  strangers and fulfilling manually; his explicit ask was hosting. The
  facilitator is the productization of what he is doing by hand.

## 4. Execution Model

The facilitator runs seller workloads through mitos's transport preference
order (`01_PRODUCT.md` §8), with one production-specific sharpening:

- **Headless-first in the metered path.** Production paid calls run over
  structured transports (native API / headless-JSON: Claude Code
  `--print stream-json`, Codex `exec`, Gemini headless). When a stranger is
  charged per call, a provider TUI redraw is a *revenue incident*, not a
  parsing bug; screen-scraping fragility that is tolerable in a local devtool
  is an SLA breach in a metered endpoint.
- **PTY is breadth, not foundation.** The PTY/`libghostty` codec is what lets
  the facilitator package the long tail of TUI-only agent CLIs, and what
  enables human-in-the-loop capabilities (seller-side approval checkpoints
  mid-run). It widens supply; it does not carry the production happy path.
- **Per-run budgets are metering features, not afterthoughts.** Agent runs
  fail and wander in ways prompt calls do not; variance is the seller's COGS
  killer. Every packaged capability carries a token budget, wall-clock cap,
  and policy-capped action set, enforced by the runtime and reflected in the
  run record.
- The design partner's actual workload is the forcing function for which
  transport ships first.

## 5. What the Seller Gets

1. **Onboarding/packaging.** Upload the payload (prompts, process definition,
   skill files, pipeline config, dataset). The facilitator wraps it as a
   mitos harness workload.
2. **Hosting.** The workload runs in a sandboxed container under the mitos
   runtime (Nucleus enforcement), per-tenant isolated.
3. **Key custody and COGS routing.** Inference runs on the **seller's own
   provider keys**; the seller bears per-call inference cost and sets a price
   above it.

   **Compliance constraint (hard):** keys means *API keys*, consumed through
   the harnesses' API-key/headless modes. The facilitator never automates a
   consumer-subscription CLI login server-side — that is the account-pooling
   pattern provider terms prohibit and `01_PRODUCT.md` §13 already forbids.
   Consequence accepted: the facilitator gets no subscription-pricing cost
   advantage; the bet is purely the **capability** advantage of harnesses
   (§2). Watch provider formalization of hosted-agent billing (e.g.
   Anthropic's 2026-06 Agent SDK billing split) as the sanctioned path.
4. **Metering and payment link.** Per-call metering, a price, and a shareable
   link/`gentix://` URI the seller posts to their own audience. The tweet is
   the discovery; the facilitator is the checkout behind it.
5. **Gentix protocol integration.** The endpoint is listed on Gentix; settled
   calls produce receipts, and the seller can opt into verification tiers
   (pay-if-good insurance) and accrue portable reputation.

## 6. Delivery Model

Harness runs break the Pickaxe interaction shape, and the product embraces
that instead of hiding it:

- **Async by default.** A run takes minutes to hours and costs the seller
  real money per call. The buyer flow is: pay the link → get a job page →
  notified on completion → artifacts + proof page. Mitos's run-report
  architecture is the job page; synchronous response is the special case for
  fast capabilities, not the default.
- **Outcome price points.** The unit is a completed job, priced in the
  $10–100 range, not a $0.30 micro-call. This matches the wedge seller's
  existing manual pricing and is a different segment from x402's observed
  API-micropayment traffic — the rails support both.
- **Pay-if-good is operationally necessary at these prices**, not a
  nice-to-have. Agent-run variance plus two-digit prices means refund/
  verification handling must exist from early on; the Gentix verification
  tier is the mechanism, and the mitos evidence pipeline is what makes a
  verdict adjudicable.
- **The proof page is a product surface.** Run report, artifact list, diff,
  redacted transcript, receipt. Pickaxe delivers a chat transcript; the
  facilitator delivers receipts.

## 7. Revenue Model

- **10–15% take-rate on hosted GMV**, charged by the facilitator and entirely
  outside the Gentix protocol fee (self-hosted sellers pay no facilitator
  fee).
- Sellers pay inference COGS directly via their own keys; the facilitator does
  not arbitrage inference.
- Per company strategy, **facilitator revenue is the business's primary
  revenue line for roughly the first two years**; the protocol take (3–5% on
  settled tasks, 0% on pass-through) stays small while the network grows.

## 8. Relationship to the Gentix Protocol

Two backend modes:

- **Gentix-settled** (default): calls settle through the protocol with
  receipts, optional verification, and reputation accrual.
- **Pass-through**: plain metered hosting with no protocol fee and no
  verification, for sellers who only want the hosting and link.

The facilitator holds **no privileged protocol access**. Multiple competing
facilitators are expected and welcomed — protocol neutrality is what makes
the Gentix registry canonical. This facilitator competes on being first and
best-integrated (Automattic model: open protocol, commercial hosted service).

## 9. Open Decision: Open-Sourcing

Undecided. The framing for the decision:

- **Engine (mitos runtime): lean open.** Community adoption of the runtime
  expands the protocol's seller base and costs the business little.
- **Hosted service (multi-tenant packaging, metering, billing, key custody):
  lean closed or open-core.** This is where take-rate revenue lives; the
  operational service is the defensible part regardless of source
  availability.

Do not open-source the hosted service before the take-rate business is
established (Docker cautionary case).

## 10. Competitive Frame

- **Pickaxe / Wordware / Glif** (no-code agent builders): wrap raw model
  APIs; synchronous chat-shaped delivery; no sandbox, no run evidence, no
  policy. Their moat is the *studio* — creator-to-checkout in an afternoon —
  not their execution layer. The facilitator differentiates on execution
  (harness jobs, sandbox, proof pages) and must still match the studio bar
  for onboarding (§13).
- **Agent37 / Agensi** (Claude-skill monetization wrappers): subscription
  wrappers around skills, no hosted sandboxed execution, no published
  traction.
- **x402 Bazaar / MCP registries**: discovery and settlement for APIs, not
  creator packaging or hosting. Complementary rails, not competitors.
- As of mid-2026, **no one ships hosted, metered, sandboxed harness
  execution for sellers**. The absorbers to watch are Coinbase, Cloudflare,
  and Stripe adding "host a creator workflow" as a feature; the defense is
  speed, registry, and integration, per `gentix` strategy §10.

## 11. MVP Scope (Design Partner #1)

Ship the smallest thing that replaces his manual fulfillment:

1. Wrap his design process as a mitos harness workload (done by us, by hand —
   concierge onboarding is acceptable and expected).
2. Configure his provider API key, set his price, set his run budget caps.
3. Give him a payment link; he tweets it.
4. A stranger pays, the workload runs, the output delivers, a receipt/proof
   page exists.

Success criterion: **one stranger-paid, machine-fulfilled transaction** —
this validates the wedge, the facilitator, the COGS model, and
tweet-as-discovery in a single event. Then repeat for the unserved requests
he already receives.

## 12. Sequencing Note

Build order for the gaps in §13: **key custody → metering → public endpoint
→ multi-tenancy**. The MVP needs the first three; concierge operations can
stand in for the fourth while there is one tenant. Standalone-product
surfaces not on this path (provider comparison, viewer polish, PTY-codec
depth beyond what the design partner's workload needs) are frozen at
whatever works today.

## 13. Gap Analysis (current mitos → facilitator MVP)

Mitos today: Zig engine + BEAM control plane that runs coding agents as
sandboxed, audited workloads with run reports — the execution and evidence
substrate is real, and under the harness positioning (§2) it is the
differentiator rather than overhead.

Missing for the facilitator:

- **Multi-tenancy**: per-seller workload isolation, quotas, and resource
  accounting at the service level (Nucleus provides the sandbox primitive).
- **Seller onboarding surface**: payload upload → workload definition,
  initially operator-assisted. Long-term this is the adoption moat — the
  studio is most of the remaining product once the runtime works (§10).
- **Key custody**: encrypted per-seller provider **API** keys injected at run
  time (§5 compliance constraint).
- **Metering and billing**: per-call usage records, pricing, settlement via
  x402/USDC, take-rate accounting — including per-run token budgets and
  wall-clock caps as first-class metering objects (§4).
- **Public call surface**: the Gentix-spec-compliant endpoint + payment link
  resolution (depends on `gentix` packaging spec, tracked there as
  `13_CAPABILITY_PACKAGING.md`).
- **Buyer delivery**: async job page + artifact handoff + proof page per the
  run report/artifacts mitos already produces (§6); synchronous response as
  the fast-path special case.

These gaps are the build backlog; sequencing per §11/§12 — concierge first,
productize what the concierge work proves necessary.
