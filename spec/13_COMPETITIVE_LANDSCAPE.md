# 13 — Competitive Landscape

Status: draft v0.1 · Date: 2026-06-30

A **descriptive** map of the agent-framework / agent-runtime space as of
late June 2026, scoped to what each player **is** and how it overlaps with
mitos's specified surface. It does **not** propose direction changes, pick
winners, or revise `02_PRODUCT`. Where overlaps are noted, they are facts
about surface area, not commitments. Direction decisions live in `02` (risks,
wedge), `08` (facilitator), and `09` (sequencing).

Where a claim here depends on a mitos concept, it cites the owning spec; where
it depends on a third party, it cites the source. RFC 2119 keywords are not
used here — this is a map, not a contract.

---

## 0. How to read this

For each player: **what it is**, **what first-class concepts it exposes**,
**overlap with mitos** (concept-by-concept), and **what it is not** (where mitos's
specified surface extends beyond it). The set of players is deliberately short —
it covers the adjacent categories (framework, hosted runner, marketplace) rather
than every prompt-wrapper.

The mitos-side vocabulary used below is fixed by `01` (capability, pipeline,
sandbox, event log, trust tier, authority boundary) and `02` (substrate vs
facilitator, proof page, completed-work monetization).

---

## 1. Vercel Eve — filesystem-first durable agent framework

- **Source:** `github.com/vercel/eve`, npm `eve`, `npx eve@latest init`.
  Open source (repo public on `vercel/eve`). Launched late June 2026.
- **Deployment model:** authored locally, deployed/hosted on Vercel (or
  self-hosted). Single-tenant by construction: the agent runs against *the
  deployer's* account and resources.

### 1.1 What it is

A TypeScript framework where **the filesystem is the framework**: an `agent/`
folder is the deploy unit, and the framework discovers and wires its contents at
build time into durable workflows. Conceptually the Next.js move applied to
agents — convention over config, file-based routing as authoring.

### 1.2 First-class concepts inside `agent/`

| Eve convention | Authoring | Role |
|---|---|---|
| instructions | markdown | agent instruction set |
| `skills/` | markdown | units of agent capability |
| `tools/` | TypeScript (`defineTool`) | callable tools |
| `channels/` | code | communication/integration channels (Slack template ships) |
| `connections/` | code | external service connections |
| `sandboxes/` | config | per-agent sandboxed compute (Vercel Sandbox or Sandbox0) |
| `subagents/` | markdown/code | nested agents |
| `schedules/` | config | scheduled tasks |
| `evals/` | config | evaluation harness |

Runtime: durable sessions/workflows compiled from the folder; one sandbox per
agent; built-in approvals; durable state for resumption.

### 1.3 Overlap with mitos (surface, not commitment)

| Mitos concept | Eve equivalent | Note |
|---|---|---|
| capability = `(data + skill + tools)` in an OCI image (`01 §1.1`, `03`) | `agent/` folder of instructions + `skills/` + `tools/` | same atom, lighter DX; Eve has no OCI / content-addressed packaging |
| pipeline = markdown composition enforced by an **orchestrator agent** at runtime (`04`) | durable **workflows** compiled from the folder (declarative) | both are "compose capabilities," different control model: Eve leans declared-FSM; mitos bets reasoned policy + deterministic record (`01 §0`) |
| I/O plane: connectors in / sinks out (`10`) | `channels/` + `connections/` | near-identical surface; Eve ships Slack out of the box |
| compute plane: T2 µVM, deny-all egress, RO snapshot (`01 §6`, `05`) | per-agent sandbox (Vercel Sandbox / Sandbox0) | same primitive; Eve's trust story is single-tenant / own-workload, no cross-tenant isolation requirement |
| application manifest + OCI packaging (`12`) | the `agent/` folder **is** the deploy unit | Eve is simpler and lighter; mitos is more formal (`app_id`, entrypoints, default I/O bindings, service composition) |
| event log = state + audit + proof + billing ledger (`01 §8.2`) | durable session state (for resumption) | different purpose: Eve's durability is **operational** (resume), not evidentiary or billable |

### 1.4 What Eve is **not**

- **No cross-tenant monetization.** No metered public endpoints, no settlement,
  no take rate. An Eve agent serves its deployer; it does not sell completed jobs
  to strangers. (Mitos facilitator: `08`.)
- **No seller-key custody with a server-side recipe.** The deployer's keys and
  recipe are theirs; there is no broker that attaches/withholds credentials per
  `egress_policy` against a buyer's data (`01 §8.5`, `05`).
- **No proof pages.** No diff + `RESULT` + artifacts + replayable trace delivered
  as evidence to a buyer (`01 §8.5`, `04`). Eve's durable state is internal.
- **No event-log-as-billing-ledger.** The log is for resumption, not metering.
- **Single-tenant trust model.** The sandbox protects the deployer from the
  agent; it is not the cross-tenant authority boundary mitos specifies (`01 §6`).

---

## 2. Adjacent categories (not yet mapped in detail)

Listed as placeholders so this doc owns the slots; each gets a subsection when
it is mapped. Additions here should be players that occupy an adjacent
**category**, not one-off wrappers.

- **2.1 Single-prompt / single-API-call wrappers (Pickaxe class).** Sells a
  templated model-API call. Mitos's stated counter-position: sell the output of a
  full harness in a sandbox, not a chat turn (`02 §1`).
- **2.2 Authored-pipeline / FSM orchestrators (e.g. chief-wiggum).** A fixed
  stage machine that compiles tasks → artifacts through pre-authored stages.
  Mitos's stated counter-position: replace the authored stage machine with a
  reasoning orchestrator + deterministic record (`02 §2`).
- **2.3 Coding-agent harnesses (Claude Code, Codex, Gemini CLI, OpenCode, pi).**
  The **cognitive layer**, not competitors; mitos wraps them as pluggable
  harnesses (`01 §1.4`, `01 §8.6`). Tracked as suppliers, not adversaries.
- **2.4 Hosted agent runtimes / sandbox providers (E2B, Daytona, Modal, Vercel
  Sandbox, Sandbox0).** Compute-plane suppliers (`05`), not competitors.
- **2.5 Model gateways / routers.** Provider-routing primitives; relevant to the
  egress broker (`01 §8.5`) but not a substitute for the substrate.

---

## 3. Where mitos's specified surface is, today, uncontested

Restated from the specs above as a fact, not a plan — these are the parts of the
mitos design for which no mapped player offers an equivalent surface:

- **Cross-tenant paid completed work:** metered public endpoint + settlement
  (USDC/x402) + facilitator take on a transaction between strangers (`08`).
- **Seller-key custody with a server-side recipe:** broker attaches/withholds
  credentials per `egress_policy`, no secrets in the sandbox (`01 §8.5`, `05`).
- **Proof pages as the deliverable:** evidence bundle + replayable trace built
  from the event log (`01 §8.5`, `04`).
- **Event log as the billing ledger:** metering reads the same record that is
  run state, audit, and proof (`01 §8.2`, `08`).
- **T2 remote-µVM cross-tenant isolation** with deny-all egress and RO input
  snapshot (`01 §6`, `05`).

These are uncontested as of this doc's date; they are not asserted to be
defensible forever. Updates to this map go here, not in `02` or `08`.

---

## 4. Cross-references (no direction decisions)

- Thesis, wedge, success gate, risks: `02_PRODUCT` (§1, §6, §8).
- Facilitator (monetization, custody, proof delivery): `08_FACILITATOR`.
- Canonical contracts (capability, pipeline, sandbox, event log, authority):
  `01_ARCHITECTURE` §8.
- Sequencing / MVP scope: `09_IMPLEMENTATION_PLAN`.

This document carries **no decisions**. Any implication that mitos should adopt,
integrate, compete with, or ignore any player above is out of scope here and
belongs in `02`, `08`, or `09`.
