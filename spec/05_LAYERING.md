# LAYERING.md — Control Plane / Engine Split

Status: draft v0.1
Date: 2026-06-04

> **Naming:** `00_ARCHITECTURE.md` is the authority for the word "mitos" — it
> denotes the *whole* AI-native OS (entry surfaces + orchestrator + engine +
> authority + memory + trace, with Nucleus as the enforced boundary). This
> document describes one internal seam of that OS: the boundary between the
> orchestrator (control plane) and the **mitos engine** (the agent-runtime /
> transport subsystem, §2.3 of `00_ARCHITECTURE`). Where earlier drafts said
> "mitos = the transport layer," read "the mitos engine."

This document records the internal layering decision: a deterministic control
plane (BEAM) wrapped around a non-deterministic agent engine (Zig), connected by
a single versioned protocol. It supersedes the implicit two-runtime split
described in `02_HLD.md` §3 by assigning explicit roles and a single run
lifecycle.

---

## 1. Locked decisions

1. **Two layers, one boundary.** Control plane (BEAM) and the mitos engine
   (Zig), connected by a single versioned protocol. No third runtime, no second
   lifecycle state machine.
2. **BEAM is the orchestrator / control plane** for now, on the assumption it
   runs co-located on the server. Standalone/packaged distribution is an
   explicit *non-goal for now* and a known cost (BEAM is awkward to ship
   standalone). The boundary is drawn so BEAM is swappable later without
   touching the engine. (It is a *subsystem* of mitos, not a separate thing
   mitos plugs into — see `00_ARCHITECTURE` §1.)
3. **The mitos engine (Zig) is the agent-runtime / transport subsystem.** It
   owns *all* transport implementations and the mechanism of selecting among
   them. The orchestrator never names a transport implementation — it states
   intent and preference order. ("Mitos" as a whole is the OS, not this engine;
   `00_ARCHITECTURE` §2.3.)
4. **Policy is split by "decide vs enforce," not by layer.** The orchestrator
   decides; the engine enforces what has been compiled down to it plus hard
   synchronous safety invariants. See §4.

---

## 2. Layer responsibilities

### 2.1 BEAM — Control plane / orchestrator

- Run lifecycle state machine (the *single* one — Zig's `orchestrator.zig`
  lifecycle is demoted, see §8).
- Policy *decisions*: allow/ask/block, network egress, mount boundaries,
  resource limits.
- Approval controller (human/UI/watcher routing).
- Canonical trace store writer (`events.jsonl`, `run.yaml`, report).
- Authority plane service (§6).
- Web/API surface, session registry, multi-run scheduling.
- Provider *selection* (names a provider + config + transport preference order).

### 2.2 Zig — the mitos engine (agent-runtime / transport subsystem)

- Transport registry + implementations (§3).
- Provider registry: argv construction, codec, prompt detectors — *all*
  provider-specific knowledge moves here.
- Terminal core (libghostty-vt) as one codec, not the spine.
- Sandbox invocation (hands Nucleus the compiled launch config; does not author
  it).
- Synchronous policy *enforcement* (§4 Tier 2) + raw byte spool + first-pass
  redaction.
- Emits one unified typed-event vocabulary upward regardless of transport.

The orchestrator's mental model of the engine is: *"I hand you a LaunchSpec and
a compiled policy; you give me back a typed event stream and accept control
commands."* It does not know whether that is a PTY, a JSON stream, or a cloud
job.

---

## 3. The mitos engine as the agent transport layer

### 3.1 Decompose the flat `AdapterMode` into three orthogonal axes

Today `native_api | headless_json | exec_json | pty | raw_shell`
(`src/adapters/types.zig`) conflates transport, codec, and lifecycle. Replace
with three independent axes:

| Axis | Options | Owns |
|---|---|---|
| **Transport** (byte movement) | `stdio` · `pty` · `socket(json-rpc)` · `http-stream` · `cloud-job` | spawn/connect, send, cancel, resize |
| **Codec** (interpretation) | `text` · `json-stream` · `acp` · `mcp` · `vt-screen` | bytes/frames → TypedEvent |
| **Lifecycle** | `one-shot` · `durable-session` | reattach semantics, resume |

A named transport is a *composition* of these axes:

```text
pty        = pty        + vt-screen   + durable-session
stdio-json = stdio      + json-stream + one-shot
acp        = socket     + acp         + durable-session
mcp        = socket     + mcp         + durable-session
cloud-job  = http-stream + json-stream + durable-session(reattach)
http-stream = http-stream + text/json-stream + durable-session
```

Adding a transport is composing existing axes, not writing a monolith.

### 3.2 The Transport interface (Zig)

```zig
// Transport (vtable)
//   detect(provider, env) -> Capabilities
//   open(LaunchSpec) -> TransportSession
//
// TransportSession (vtable)
//   send(Input)            // bytes for pty/stdio; structured msg for json-rpc/mcp
//   resize(cols, rows)     // pty-meaningful, no-op elsewhere
//   cancel(reason)
//   poll() -> []Frame      // raw or structured, fed to the codec
```

A single **engine loop** owns: sandbox handle + child process + chosen Transport
+ Codec. It pumps `Transport.poll → Codec → TypedEvent → protocol-up`. The codec
is where vt-screen-parse / json-stream-parse / acp-parse live. The prompt
detector becomes part of the `vt-screen` codec, not a top-level component.

### 3.3 Provider knowledge moves down

The engine gets a provider registry:
`provider → (argv builder, transport preference, codec, prompt rules)`. The
orchestrator passes `{provider, task, config, transport_order}`; the engine
resolves availability and picks the highest-preference *available* transport.
This is the principle that the orchestrator must not care about implementations
below it.

**Consequence:** the BEAM `Mitos.Adapter.*` modules and `Mitos.PromptDetector`
are deleted (§8).

---

## 4. The policy split

Model: **policy is decided in the control plane, compiled, and enforced in the
engine.** Three tiers.

| Tier | What | Decided by | Enforced by | Mutable mid-run? |
|---|---|---|---|---|
| **1 — Launch** | network mode/egress, fs mounts & rw boundaries, resource limits, env allow/deny, credential mounts | Orchestrator | Sandbox (Nucleus) at launch | No (requires restart) |
| **2 — Synchronous invariants** | input-injection gating, escape/OSC/clipboard sanitization, outbound secret redaction, frame/size validation, *compiled auto-allow patterns* | Orchestrator authors; engine enforces | Engine, no round-trip | Compiled, not live |
| **3 — Decisional** | run_command, install_package, git_push, write_outside_workspace, read_secret → allow/ask/block | Orchestrator (may need human/watcher/durable context) | Engine performs only after receiving an authorization token | Yes, per-action |

**The rule that makes it clean:** the engine *never* makes a discretionary
allow/ask/block decision. It either (a) enforces a rule the orchestrator
compiled down (Tier 1/2), or (b) bubbles up an `ACTION_REQUESTED` event and
blocks until the orchestrator returns an authorization token or denial (Tier 3).
The "prompt detector never presses keys itself" invariant
(`02_HLD.md` §5.3) is now structural: input injection requires a token that only
the control plane mints.

**Hot-path concession (policy compilation):** routine auto-allows (e.g.
`allow: run_tests`) would be slow if every one round-trips. The orchestrator
compiles its `allow` patterns into a Tier-2 fast-path set at launch, so the
engine auto-authorizes matches locally and only `ask`/`block`/unknown actions
bubble up. Decisions still belong to the orchestrator (it authored the
patterns); latency stays local. This is the one place the engine "owns policy" —
it owns *enforcement of compiled policy*, never authorship.

---

## 5. The control protocol (BEAM ↔ Zig seam)

Generalize the existing sidecar framing (length-prefix + stream tag + MessagePack)
into *the* contract. Transport-agnostic vocabulary.

**Control plane → engine:**

```text
OPEN(LaunchSpec)
INPUT(bytes | structured)
RESIZE(cols, rows)
SIGNAL(signum)
AUTHORIZE(action_id, token)
DENY(action_id, reason)
SNAPSHOT_REQUEST
CANCEL(reason)
```

**Engine → control plane:**

```text
RUN_STARTED
PROCESS_SPAWNED
RAW_REF(offset, len)
SNAPSHOT
PROMPT_DETECTED
ACTION_REQUESTED(action_id, action, context)
STRUCTURED_OUTPUT
FINAL_OUTPUT
SANDBOX_EVENT
RESOURCE_SAMPLE
PROCESS_EXITED
ERROR
```

`LaunchSpec` carries: provider, task, transport preference order, compiled
Tier-1 launch config, compiled Tier-2 fast-path rules, redaction patterns,
authority-plane socket path + token. **Version this struct explicitly** — it is
the swap-point if BEAM is ever replaced.

---

## 6. The authority / callback plane

- The authority plane is a **control-plane (BEAM) service**, not an engine
  concern.
- It is exposed *into the sandbox as a unix-domain socket* at a well-known path
  (no real network needed — the default `network: none` stays intact). The
  engine's only job is to mount that socket and inject env: `OD_DAEMON_URL` (the
  socket), `OD_PROJECT_ID`, `OD_RUN_ID`, `OD_TOKEN` (run-scoped capability).
- Surface (MCP over that socket): `tool_broker` (agent requests an action →
  orchestrator runs Tier-3 policy → grant/deny), `approval`,
  `artifact_register`, `memory_fetch`, `subrun_spawn`.
- This makes the agent's requests *typed* instead of screen-scraped:
  `ACTION_REQUESTED` can now originate from a structured MCP call rather than the
  vt-screen codec guessing from pixels. Screen-scraping demotes to the fallback
  path for dumb CLIs and directly mitigates `01_PRODUCT.md` §15.1 / `02_HLD.md`
  §12.1 (terminal parsing fragility).
- `OD_TOKEN` scope maps directly onto the same Tier-3 policy the orchestrator
  already evaluates — one policy engine, two entry points (scraped prompt vs.
  brokered tool call).

**Prompt = affordance, env = authority.** The system prompt / markdown tells the
agent what it *may* do; the injected env (`OD_*`) is what makes the callback
actually work for scripts, subprocesses, retries, and cloud workers.

---

## 7. Trace & state ownership

- **Orchestrator is the canonical trace writer** (`events.jsonl`, `run.yaml`,
  report) — centralizes audit for server / multi-run / durability.
- **Engine writes only the high-volume raw spool** (`terminal.raw`) locally and
  emits `RAW_REF` offsets; first-pass redaction happens at the engine boundary
  so raw secrets never need to cross the seam.
- **Durable run state** (Phase 7) lives in the orchestrator, keyed by `run_id`,
  checkpointed from the event stream — the precondition for cancel/resume and
  later cloud-job reattach.

---

## 8. What moves / what is deleted (consolidation)

- **Delete:** BEAM `Mitos.Adapter.{Claude,Codex,Gemini,Fake}`,
  `Mitos.PromptDetector`, `PromptWatcher` provider-specifics. Provider knowledge
  consolidates into the Zig provider registry.
- **Demote:** Zig `src/runtime/orchestrator.zig` lifecycle → it stops being a
  control plane. It keeps only a thin *embedded controller* used when running
  headless with no BEAM (CI/standalone). Same engine, degenerate driver.
- **Promote:** the sidecar protocol → the official control protocol (§5).
- **Keep untouched:** Nucleus boundary, libghostty narrow wrapper
  (`src/terminal/ghostty.zig`), event-sourced substrate.
- **BEAM `Session`** stops being a PTY-sidecar owner; it becomes a
  transport-agnostic handle over the control protocol.

---

## 9. Migration phases (each independently verifiable)

| Phase | Goal | Verify by |
|---|---|---|
| **0** | Lock contracts: `LaunchSpec`, control-protocol vocabulary, `TypedEvent` schema as shared versioned types | types compile both sides; round-trip test |
| **1** | Engine: introduce `Transport` behaviour; refactor existing PTY path to `Transport.pty` behind it (no new transport) | existing PTY run still works, now emits unified events |
| **2** | Move provider registry + prompt detectors into engine; delete BEAM adapter/detector modules | BEAM names `provider` only; run still works |
| **3** | Trim BEAM to control plane; `Session` becomes protocol handle | clean layer boundary; no provider strings in BEAM |
| **4** | Policy compilation: orchestrator → Tier-1 launch config + Tier-2 fast-path; engine enforces; ask/block bubble up | auto-allow runs locally; `ask` round-trips |
| **5** | Add `stdio-json` transport (Claude `--print stream-json`) | a live *structured* run streams through BEAM to UI — proves PTY is not the spine |
| **6** | Authority plane: BEAM service over sandbox unix socket; inject `OD_*`; route one approval via MCP | agent requests a tool through typed channel, not screen-scrape |
| **7** | Durable state + cancel/resume keyed by run_id | kill mid-run, resume from checkpoint |
| **8 (deferred)** | Standalone packaging: harden Zig embedded controller; document BEAM swap-point | headless CLI runs with no BEAM |

The ordering front-loads the structural seam (0–3), then proves the thesis with
a second transport (5) before the bigger authority/durability work (6–7).
Phase 5 is the cheapest decisive proof that the layering achieved its goal.

---

## 10. Decisions still open

1. **Codec for `acp` and `mcp` as transports vs. the authority plane's MCP** —
   same protocol, two roles (agent-as-server-output vs. agent-as-client-callback).
   Decide whether one MCP implementation serves both or they stay separate.
2. **Where redaction's policy-aware second pass runs** — engine does byte-level
   first pass; does export-time redaction live in orchestrator or a shared lib?
3. **Fast-path rule expressiveness** — how much of the policy DSL is compilable
   to the engine (Tier 2) vs. inherently must round-trip (Tier 3)? Defines the
   compiler's target.
4. **Resume granularity** — checkpoint per-event vs. per-state-transition;
   affects cloud-job reattach fidelity.
5. **Transport preference resolution** — does the engine fall back silently down
   the preference order, or report each unavailable transport up so the
   orchestrator can decide?

---

## 11. References

- `00_ARCHITECTURE.md` — the constitution: mitos = the whole AI-native OS; this
  doc is one internal seam (orchestrator ↔ engine) beneath it.
- `01_PRODUCT.md` — product thesis, policy model, run artifacts.
- `02_HLD.md` — component design; this document reassigns its §3 split.
- `03_NUCLEUS_INTEGRATION.md` — sandbox launch/event contract.
- `04_IMPLEMENTATION_PLAN.md` — original Zig workstreams (pre-layering).
