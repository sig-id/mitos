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
| `LaunchSpec` / `LaunchPolicy` | compiler | trust-tier → provider+posture; T2 fails closed; resolves the workspace mode | `05` §2/§4 |
| `Workspace` | policy/copier | what enters the sandbox: gitignore-honouring clone, size ceiling with named offenders, `:in_place` opt-out (refused at T2), content-free tree fingerprint | `05` §5.1, `04` §4.3 |
| `Harness` (+ impls) | behaviour | launch/observe/steer a harness; hermetic env; the `observation` matrix | `01` §8.6, `05` §6, `14` §4 |
| `Observation.Clock` | process | the quiescence clock; emits `quiescent` Effects | `14` §2 |
| `Observation.Session` | reader | tails the harness's session artifacts → the normalized stream (interactive, no mode change) | `14` §1.1 |
| `Observation.Terminal` | seam | continuous spool + on-demand digested `snapshot`; **never** a stream | `14` §3.1 |
| `Observation.Vt` | parser | a VT state machine that is fed every byte and **emits nothing**; `feed` is pure over the byte prefix, which is what makes a screen read replayable | `14` §3.1 |
| `Observation.Pty` | transport | the BEAM-side local pty byte source (the engine owns this seam, `01` §7); feeds the Terminal and provides the steer write half | `14` §3, `01` §7 |
| `Observation.Contract` | checker | static + live adapter contracts; quarantine on failure | `14` §5 |
| `Observation.Classifier` (+ impls) | behaviour | per-harness modal-state classification; `:unknown` escalates | `14` §3.2 |
| `Steer` | transaction | snapshot → write → snapshot → verify transition, else reject + escalate | `14` §3.3 |
| `Decision.Card` | data | the one shape a human request takes; reversibility, default, TTL | `01` §8.9, `15` §3 |
| `Decision.Policy` | fold | reversibility class from act + grants; **deterministic**, no model | `15` §2 |
| `Sink.Decide` / `Connector.Decision` | impls | deliver a card / apply a resolution; idempotent, tenant-checked | `15` §5, `10` §5.3a |
| `Checkpoint` | fold | commit + snapshot + log offset + predicate verdict; `green` | `16` §3.1 |
| `Acceptance` | runner | the machine-checkable oracle, executed outside the workspace | `16` §3.2, `03` §7.1 |
| `Supervision` | FSM | restart intensity (`max_restarts`/`max_seconds`) + strategy; bounds the ladder | `16` §4 |
| `Fleet` | reader/capability | fleet oversight from **evidence only** (`evidence/1` structurally cannot return narration); restricted Decision set enforced in `Event.Decision` | `16` §5 |
| `Autonomy` | fold | autonomy ratio · unattended horizon · decision precision · rewind depth · green rate; the horizon **grant** | `16` §6 |
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

## 2.1 The Studio gate (reached before §2)

`02 §6.1` fixes a second, earlier acceptance contract on the same substrate:

> A brief accepted at night runs unattended across multiple checkpointed
> segments and produces a green, verifier-checked change by morning, with zero
> or one human decisions, each resolvable in under a minute from a phone.

It is not a subset of §2 and not a milestone toward it in the loose sense — it
is the *same machinery under a different policy* (`02 §2.1`), which is why it is
worth gating on. §2 adds payment, cross-tenant isolation, and proof-for-a-
stranger on top of a substrate that has already been shown to run without a
human. A build that reaches §2 without having reached §2.1 has never
demonstrated the "machine-fulfilled, no human in the loop" clause under real
load; it has only avoided testing it.

## 2.2 Build order

Ordered by dependency, not by appeal. Each item is unblocked by the one above.

| # | Ship | Unblocks | Spec |
|---|---|---|---|
| 0 | **Instrument, don't build.** Replay existing sessions off the log → the interrupt distribution (approve-the-boring / missing-info / judgment / rescue) | decides whether the first build is a gate, a broker, or a supervisor; seeds the eval corpus | `02` §6.1 |
| 1 | **Quiescence clock + `blocked_on_human`** | everything — without it an interactive run hangs forever and no other signal can fire | `14` §2, `04` §4.3 |
| 2 | **Session-artifact tail** (structured observation of an unmodified interactive harness) + the `observation` matrix + contract tests | Studio at all; kills the mode-change constraint | `14` §1.1, §4, §5 |
| 3 | **Decision card + reversibility policy + `decide`/`decision` pair** | unattended operation becomes safe rather than merely quiet | `15`, `10` §5.3a |
| 4 | **`acceptance` predicate, run outside the workspace** + `predicate_stall` | a non-narrative progress metric; every later mechanism is unmeasurable without it | `16` §3.2, `03` §7.1 |
| 5 | **Checkpoints + `rewind` + restart intensity** | long horizons survive their own failures | `16` §3, §4 |
| 6 | **Terminal snapshot + verified steer** | modal prompts answerable without a mode change; strictly optional to 1–5 | `14` §3 |
| 7 | **Fleet supervisor** (as a capability) | multi-run operation | `16` §5 |
| 8 | **Replay-as-eval** over the recorded corpus | every later change to thresholds, prompts, and classifiers is regression-tested | `16` §6.1 |

Items 1–5 are the Studio gate. Items 6–8 are what make it hold as the fleet
grows. §2's launch gate resumes from there with `08` unchanged.

**Ordering note.** (6) sits *after* (3)–(5) deliberately. The terminal is the
lowest-trust channel (`14 §1`) and the one whose prior implementation failed;
nothing above it may depend on it, and if it slipped indefinitely the Studio
gate would still be reachable for harnesses whose modal prompts can be avoided
or pre-answered. Building it first is how the last attempt went wrong.

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
- **Studio and Market differ only in policy.** Every mechanism is shared; the
  delta is tabulated once in `02` §2.1 and MUST NOT be re-derived, branched on
  ad hoc, or grown a second implementation. A component that exists only for one
  of the two products is a defect (`01` §1.7).
- **Run semantics never come from a screen.** Progress, tool calls, usage, and
  every orchestrator input are read from the effect surface and the harness's
  own structured record; the terminal answers only modal state, on demand, at a
  quiescence edge (`14` §1, §3).
- **Reversible fails safe, irreversible fails secure.** A decision default may
  fire only on `internal`/`recoverable` acts; `external` acts park indefinitely.
  Unclassifiable reversibility is `external` (`01` §8.9, `15` §2.1).
