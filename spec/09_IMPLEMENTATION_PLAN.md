# 09 — System Decomposition

This doc normatively decomposes the mitos control plane into its components and
fixes the **launch gate** — the acceptance contract the whole system must
satisfy. It owns no field names or signatures (those live in `01` §8 and
`03`–`08`); it maps components to the specs that own their contracts.

mitos is the BEAM/Elixir control plane (`01` §0). The engine, Nucleus (and other
sandbox providers), and the sigid identity provider are **external products**
integrated through seams (`01` §0, §7); mitos constrains the seam, not their
implementation.

## 1. Component map

Every component is a fold over the log, a behaviour seam, or a reader. The
substrate is a thin kernel; pluggable concerns are `@behaviour`s, never inline
branches (`01` §10.12).

| Component | Kind | Owns | Contract |
|---|---|---|---|
| `Event.{Decision,Effect}` | data | the two canonical log record types | `01` §8.1/8.2 |
| `Log` | writer | append-only; single-writer-per-run; fsync-before-execute | `01` §4 |
| `Run.State` | fold | `state = fold(events)`; pending-Decision recovery tail | `01` §4, `04` §7 |
| `Run` (control loop) | process | step runtime (hooks → loop → harvest); budget refusal; resume/re-drive | `04` §3.4/§7 |
| `Orchestrator.Policy` (+ impls) | behaviour | decides the next Decision | `04` §3 |
| `LoopManager` | FSM | intra-step iteration; escalation-signals seam | `04` §4 |
| `Pipeline` | parser | loose markdown intent → ordered stages | `04` §8 |
| `Capability.{Manifest,Skill,Result}` | data | content-addressed identity; skill resolution; soft result+evidence | `03` §8/§9, `01` §8.3 |
| `Hooks` | fold | deterministic pre/post gates, Effect-logged | `03` §6 |
| `SandboxProvider` | behaviour | prepare → start → stream/control → harvest → stop | `01` §8.4, `05` §2 |
| `Provider.Local` | impl | in-process T0/T1 isolation; git-baseline harvest | `05` §3 |
| `LaunchSpec` / `LaunchPolicy` | compiler | trust-tier → provider+posture; T2 fails closed | `05` §2/§4 |
| `Harness` (+ impls) | behaviour | launch/observe/steer a harness; hermetic env | `01` §8.6, `05` §6 |
| `Hermetic` | helper | full-env-clear subprocess env; host git/gpg neutralized | `05` §6 |
| `Broker` (+ listener, destinations) | process | egress credential broker: default-deny, attach-at-edge | `01` §8.5, `05` §5.3 |
| `Vault` | store | credentials exist only here, keyed `(scope, dest)` | `08` §3.2 |
| `Meter` | fold | charge = fold over the log (take rate, floor, refunds) | `08` §4 |
| `Proof` | fold | buyer proof from Effects only; log-hash anchor | `08` §5 |
| `Settlement` (+ impls) | behaviour | the payment rail: verify / escrow / split / refund | `08` §7 |
| `Facilitator` | process | publish → quote → admit → run → settle | `08` §7–§9 |
| `Task` / `WorkPool` | fold/store | durable Task intake; disciplines/selectors | `01` §8.7, `06` |
| `App.Manifest` | data | application bundle; content-addressed `app_id`; entrypoints | `01` §8.8, `12` |
| `Connector` / `Sink` | behaviours | I/O-plane ingress / egress | `10` §3/§5 |
| `Web.Router` / `Web.*` | reader | HTTP surface = the same backend ops the CLI calls | `01` §10.11, `08` §8, `11` |
| `CLI` | reader | escript: run / log / status / proof / publish / serve | `11` §6 |
| Identity | **external** | sigid asserts operator/seller/buyer identity; mitos consumes assertions | `01` §0 |

## 2. The launch gate

mitos is **launched** — it satisfies the product contract (`02`, `08` §9) — when
this end-to-end acceptance holds:

> A stranger pays for a capability; mitos admits the payment, runs the capability
> to completion in an isolated sandbox, harvests its artifacts, meters the run as
> a fold over the log, settles the charge (seller split + take rate), and serves
> the buyer a proof page recomputable from the log excerpt. Every step is on the
> record; the commercial proof physically cannot contain the seller's recipe.

This is the normative acceptance contract. A subset that omits payment,
isolation, or proof is not "launched"; it is an in-progress build of one.

**Trust posture at launch.** Sellers are curated and runs execute at **T1** in
`Provider.Local` (process isolation). Open, untrusted-seller onboarding requires
a registered **T2** `SandboxProvider` (Nucleus / E2B / Daytona); the launch
policy **fails closed** rather than downgrading a T2 request to local isolation
(`05` §4). Buyer identity for a purchase is established by **sigid**; "buy is a
URL" (`11`) means a buyer needs no account beyond the payment itself.

## 3. Cross-cutting invariants

These thread every component and are normative (`01` §4):

- **Tenancy is stamped at creation, never retrofitted.** Every run carries its
  tenant on the `run_id` namespace from creation; every read is a tenant-filtered
  fold. There is no parallel per-tenant store.
- **The log is the only trace.** `:telemetry` spans MAY decorate Decision→Effect
  commit, broker crossings, and provider lifecycle for latency/cost queries, but
  they are a derivative; the log is canonical. A second trace store is a defect.
- **Keys live only at the broker / `Vault`.** Secrets never enter the sandbox env
  and never appear on the log — only credential *references* and
  brokered-crossing Effects.
- **All surfaces call the same backend operations.** UI, CLI, API, and automation
  invoke identical folds and processes (`01` §10.11); they differ only in
  rendering and redaction cut.
