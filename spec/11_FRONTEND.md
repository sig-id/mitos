# 11 — Frontend Product Spec

Status: draft v0.1 · Date: 2026-06-24

Scope: **how mitos is represented to a user and how it should feel** — the
product surface, not new functionality. Functionality is fixed by `01`–`08` and
the I/O plane (`10`); this doc specifies the *surfaces*, the *user journeys*, and
the *altitude* of platform involvement. It defines no new canonical contracts.
Where this doc and `01` disagree, `01` wins. RFC 2119 keywords are normative.

Sections are **[TARGET]** unless marked **[I]** (implemented today). The current
Svelte "Operations Console" (`web/`) is a 1:1 projection of backend endpoints and
is **superseded** by this spec.

---

## 1. The spine — one event log, three surfaces, two roles

Everything renders the **same event log** (`01 §8.2`). Run & Watch is a *live
fold*; Build & Test is *a run + a judge* over it; the Fleet is *many folds + Task
lifecycle + I/O deliveries*. The frontend never invents state — it projects the
log (`01 §10.11`: UI, CLI, API, connectors all call the same backend ops).

```
                 EVENT LOG (01 §8.2)  ·  I/O PLANE (10)
        ┌──────────────┬───────────────────┬──────────────────┐
     live fold     run + judge        many folds + tasks + I/O
        ▼              ▼                     ▼
   RUN & WATCH     BUILD & TEST            FLEET
   (consume+dev)   (developer, CLI-first)  (operate, web-primary)
```

| Role | Primary surface | Journey |
|---|---|---|
| **Consumer** (casual) | Run & Watch | take a packaged app → run one of its pipelines → watch → receive output |
| **Developer** (author) | Build & Test (+ Run & Watch) | scaffold → author skill/tools/data in their own tools → test locally → publish |
| *either, as operator* | Fleet | host applications, wire I/O (`10`), watch a fleet of pipelines do work |

---

## 2. The unit hierarchy — application ⊃ pipelines ⊃ capabilities

The single most important framing, and one the prior specs left implicit:

> **What ships is an *agentic application*. What a user *runs* is a *pipeline*.
> What a pipeline composes is *capabilities*. A user never picks a bare
> capability — always a pipeline, even a single-step one.**

| Level | Is | Packaged as | Who picks it |
|---|---|---|---|
| **Agentic application** | a bundle of one or more **pipelines** + the **capabilities** they reference + an app manifest (entry pipelines, I/O bindings `10 §4`, service/trigger defaults) | **OCI** (§5) | installed/added by a user (paste a git URL, §3) |
| **Pipeline** (`04`) | a markdown composition of capabilities, orchestrator-enforced | markdown in the app | **run by the user** (the runnable unit) |
| **Capability** (`03`) | the `(DATA+SKILL+TOOLS)` atom under an injected harness | OCI image + RO snapshot | never picked directly; composed into pipelines |

An application can be as simple as **one single-step pipeline wrapping one
capability**, or a rich bundle of many. The app manifest names the **entry
pipelines** a user/connector may run and the default **I/O bindings** (`10 §4`).

---

## 3. Discovery is out of scope (no marketplace here)

Discovery/marketplace is **Gentix** (`08`) and is **not** part of this product.
To add an application a user **pastes a git URL** (or points at a local
directory, §5). No catalog, no ratings, no search. The frontend lists only what a
tenant has explicitly added.

---

## 4. Surface 1 — Run & Watch (two zoom levels)

The shared core, for both the casual consumer and the developer testing.

**Journey.** Add an app (§3) → pick one of its **pipelines** → provide intent +
inputs (the input being the I/O-plane's input side, `10 §3`) → **watch** → receive
output (the I/O-plane's output side, `10 §5`). For the casual user this is the
whole product: *give intent, watch the orchestrator pull capabilities in and
work, get a result.*

**The watch experience has TWO core screens** — outer and inner — because the
event log is the truth for the *outer* layers, but the inside of a capability
needs finer detail than the orchestrator ever folds:

| | **Outer — pipeline timeline** | **Inner — capability trace** |
|---|---|---|
| Shows | the run as Decision/Effect events: orchestrator moves (`advance/insert/retry/switch/skip…`), capability boundaries, harvests, I/O deliveries | each step the **agent harness** takes inside one capability: `assistant_text · tool_use · tool_result · token_usage · status` (`01 §8.6`) + raw spool |
| Source | `fold(event log)` (`04 §6`) — what the orchestrator itself reads (`04 §3.1`) | the **normalized harness stream + raw spool, owned by the Zig sidecar** (`01 §7`, `05 §5.2`) |
| Altitude | the whole pipeline | drill into one running/finished capability |
| Audience | anyone watching | debugging, trust, "what exactly did it do" |

The inner fine-trace is explicitly a **Zig-sidecar responsibility** (it owns local
transports, the terminal codec, and the raw spool — `05 §5.2`); the UI streams it
on demand when a user drills into a capability. The orchestrator still consumes
only the *folded outer* state (`04 §3.1`) — the inner trace is a human
observability surface, not an orchestrator input. No contradiction with `04`.

**Steer.** Approve / answer / interrupt / select / prompt (`05 §5.2`'s
`control`); `request_approval` parks for the user (`04`). The user can step in
mid-run from either screen.

**Output.** On finish, the result + artifacts + a replayable trace are delivered
through the configured sinks (`10 §5`) — the web app being one of them (`10 §6`).
Locally this is the proof page (`08 §7`) minus the money.

**Feel.** A CI/build run whose rows are *agent decisions*, with a drill-down into
the harness's own keystrokes, and a Stop/Approve button that actually works.

---

## 5. Packaging lifecycle — dir for dev, OCI to ship

OCI is the canonical packaging (`03 §9` — content-addressed, immutable,
replayable, meterable). The lifecycle:

```
author (dir/git, §6 CLI) ──run/test locally──► publish ──► OCI image(s) + RO snapshot
   files on disk; no build in the loop          one explicit freeze point (identity, replay, metering begin)
```

- **Local dev/run/test:** a capability/application is a **directory / git repo**;
  run and test from the path directly — no build step in the daily loop.
- **Ship/host:** an explicit **publish** content-addresses to **OCI** — the
  canonical artifact a Consumer adds and the Fleet runs.

---

## 6. Surface 2 — Build & Test (CLI-first; contract + scaffold + test)

The platform is **thin** here by decision: it does **not** author. The real
authoring is done in the developer's own editor or coding agent. The platform
provides exactly three things, **CLI-first** (`mitos …`), with the web UI as a
read-only mirror:

**(a) The contract — codified, bidirectional, always in sync with the manifest.**

| The dev MUST provide (`01 §8.3`, `03`) | The platform GUARANTEES at runtime (`03 §6`) |
|---|---|
| SKILL — anchored markdown (`@skill:role/rules/procedure/success`) | `/workspace` RW (input cloned in); `/data` RO snapshot |
| TOOLS — typed, deterministic CLI/py/js | `/skill/resolved.md`; `/tools` on PATH |
| DATA — RO corpus (optional) | a harness **injected** (never baked), switchable |
| manifest — harnesses, trust_tier, egress_policy, result schema+evidence, hooks | egress = deny-all-except-broker; **no secrets in env** |

**(b) A scaffold.** `mitos init` emits a contract-valid skeleton (anchored
`skill.md`, a typed-tool template, manifest, `result.schema.json`, hooks) so a
human or coding agent starts from something that already satisfies the contract.

**(c) The test harness — local run + trace + orchestrator-as-judge.** `mitos
test` runs the capability/pipeline in a **local T1 sandbox** (`05 §3`,
Nucleus/Docker) under a **selectable** harness → streams the two-zoom trace (§4) →
harvests result+evidence → applies **deterministic checks always** + has the
**orchestrator/verifier judge** the soft result against the declared
`success_criteria` (`04 §10`), emitting a `verification_recorded` verdict. Two
payoffs:

- a fast "does it run, and is it any good?" loop with no marketplace wiring;
- the judge's verdict is the **same signal** that feeds verify→distill and the
  per-model skill-adapt seam (`03 §2.1`, `04 §10`) — testing produces the
  improvement signal, not a throwaway pass/fail.

**Cross-harness / nested-harness — the worry, answered.** "Can it call Claude
Code inside Codex; does it have access?" A capability that needs *another* agent's
help should **`invoke` a sub-capability** (`06 §5`) — a governed, separately
sandboxed, metered call — not shell out to a second harness ad hoc. The test
harness makes this legible: `invoke_requested`/`subtask_returned` Effects show in
the outer trace, and the judge can flag missing access. v1 runs **one selectable
harness**; re-running under each declared harness is manual; the **auto cross-
harness matrix is deferred** (an explicit non-goal for v1).

---

## 7. Surface 3 — the Fleet (operate via the I/O plane; web-primary)

The layer that felt missing is **not a "factory"** — it is **I/O**. The
experience to present:

> **a fleet of agentic applications (pipelines) doing stuff** — fed by connectors,
> emitting to sinks, observable on the web.

**What it renders** (mostly a projection of `10` + `06`):

- **Applications & their pipelines** the tenant has added (§3), each with its **I/O
  bindings** (`10 §4`): which connectors feed it, which sinks it emits to.
- **The fleet board** — running services/pipelines, their pools and disciplines
  (`06 §2`), the Task lifecycle (queued/running/parked/done/failed — `06 §3`),
  health + circuit-breaker state (`06 §8`), and the **"why is it on"** resolved
  active set (`06 §9.4`).
- **I/O activity** — recent ingress (signals → Tasks, `10 §3`) and egress
  (deliveries, `10 §5`), each an Effect on the log.

**Feel.** A process supervisor for agents — units, status, logs (= log slices),
enable/disable, dependency/why — but framed as *applications doing work for me*,
with the inputs and outputs visible at the edges.

**Web-primacy here.** Post-deployment "operate & watch" is the web app's home
turf (the web app is itself one I/O surface, `10 §6`). This is distinct from the
CLI-first build loop (§6).

---

## 8. CLI vs web primacy

| Phase | Primary | Why |
|---|---|---|
| **Build & Test** (developer, local) | **CLI** (`mitos init/run/test`), first-class | the developer "runs everything locally"; the web UI mirrors read-only |
| **Run / Operate / I/O** (post-deploy, usage) | **web**, with CLI parity | watching a fleet, receiving outputs, steering — a screen experience |

Both call the same backend ops (`01 §10.11`); primacy is about *which surface is
the default for that phase*, not capability gating.

---

## 9. Status & deployment scope

| Now (v1) | Future extension |
|---|---|
| **local** sandboxes — Nucleus / Docker, **T1** (`05 §3`) | **remote** sandboxes — E2B / Daytona, **T2** (`05 §3`) — *not implemented today* |
| dir-based dev; OCI on publish (§5) | hosted multi-tenant T2 pool (`08 §3`) |
| `webhook` + `manual`/`web` I/O; `explicit` selection (`10 §8`) | `chat`/`email`/`calendar` connectors; `smart` selection |

Running pipelines in Daytona/E2B containers is a **future** extension of the
product; the v1 frontend targets the **local** runner.

---

## 10. What this spec owns vs delegates

| Owns (this doc) | Delegates |
|---|---|
| the three surfaces, the two roles, the two-zoom watch, CLI/web primacy, the app⊃pipeline⊃capability framing | the event log + control model (`01`, `04`); the capability contract (`03`) |
| the build altitude (thin: contract + scaffold + test) | the sandbox/test execution (`05`); verify/judge (`04 §10`) |
| the Fleet *presentation* | the I/O *mechanism* — connectors/sinks/bindings (`10`); pools/services (`06`) |

---

## 11. Open questions

1. **Run entry (`10 §3.3`):** explicit pipeline pick vs smart selection from an
   app's entry pipelines — decided by trying `explicit` first.
2. **Inner-trace fidelity:** how much raw harness spool to surface vs redact in
   the inner screen (`05` redaction) for non-developer consumers.
3. **App manifest shape:** the exact fields of the application bundle (entry
   pipelines, default I/O bindings, service defaults) — to pin alongside `03`'s
   capability manifest and `04`'s pipeline format.
4. **Web/CLI parity surface:** which operate actions are web-only vs need CLI
   parity for headless/automation.
