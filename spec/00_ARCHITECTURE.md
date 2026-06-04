# 00_ARCHITECTURE.md — Mitos as an AI-Native Operating System

Status: draft v0.1
Date: 2026-06-04

This is the top-level architecture for mitos. It folds the generic AI-native
program architecture (originally drafted as gentobox `spec/01_ARCHITECTURE.md`)
into mitos and specializes it to this system.

It establishes the constitutional claim that recontextualizes every other spec
in this directory:

> **Mitos is not the transport layer. Mitos is the operating system.**

Earlier specs scoped the word "mitos" to the Zig agent-transport engine
(`05_LAYERING.md` §1.3). This document promotes "mitos" to denote the *whole*
deterministic AI-native runtime — entry surfaces, orchestrator, agent runtime,
tools/authority, memory, artifacts, and the sandbox boundary — and demotes the
Zig transport engine to one subsystem within it (§2.3). `05_LAYERING.md` is
updated to match.

The core claim, inherited unchanged:

> An AI-native operating system keeps authority, state, orchestration, and
> effects in deterministic code, while using agents as stochastic execution
> workers.

Agents plan, generate, inspect, repair, and operate tools. Mitos owns what is
allowed, where state lives, how effects are recorded, and when a run advances.
The deterministic layer does not try to make the model predictable; it makes the
model's effects inspectable, bounded, replayable, and recoverable.

---

## 1. The reframe

| Term | Before (`05_LAYERING`) | Now (this doc) |
|---|---|---|
| **mitos** | the Zig agent transport layer | the whole AI-native OS |
| the Zig engine | *is* mitos | the **mitos engine** — agent-runtime subsystem (§2.3) |
| BEAM | a swappable control plane mitos plugs into | the **orchestrator** subsystem of mitos (§2.2) |
| Nucleus | an external sandbox | the **enforcement boundary** of mitos (§2.6) |
| authority plane | a BEAM service | a first-class OS subsystem (§2.5) |
| trace store | output of the run | the OS event log + artifact store (§2.8) |

Nothing in the locked layering decisions is reversed. The control-plane / engine
seam, the three-tier policy split, the versioned control protocol, and the
Nucleus boundary all stand. What changes is the *scope of the name*: those layers
are no longer "things around mitos," they **are** mitos.

The daemon — orchestrator + engine + sandbox + authority + trace + memory, taken
together — is the program. The UI is a client. The CLI is a client and a launch
surface. Agents are workers.

---

## 2. Architectural layers

At a high level:

```text
User / API / Scheduler
  -> orchestrator            (BEAM control plane: lifecycle, policy, approvals)
    -> mitos engine          (Zig: transport x codec x lifecycle)
      -> agent process       (pty | stdio-json | acp | mcp | cloud-job | http)
        -> tools             (authority plane / MCP over unix socket)
        -> raw shell         (escape hatch)
          -> Nucleus sandbox (gVisor: enforces launch authority)
            -> trace store    (events.jsonl, run.yaml, report, artifacts)
```

The system has two kinds of computation:

1. **Deterministic** (owned by mitos): run lifecycle, routing, policy decisions,
   tool execution, capability grants, sandbox enforcement, trace writes, retries,
   cancellation, resume, artifact persistence.
2. **Non-deterministic** (delegated to agents): interpreting intent, planning,
   generation, critique, repair, search/synthesis, and deciding which tools to
   call *within granted authority*.

### 2.1 Entry surfaces

How humans, scripts, and other systems submit work. All of them call the *same*
backend operations:

- CLI: `mitos run` / `atr run`, `compare`, `report` (`01_PRODUCT` §7.1).
- Web / WebSocket run viewer (xterm.js replay, diff, approval timeline).
- Runtime daemon (session registry, live state, approval prompts).
- Project config: `.agent-runtime.yaml`.
- MCP server surface (future — `01_PRODUCT` §18 Q7).
- Scheduled / CI / batch invocation.

A capability that exists only in the UI is not composable; one that exists only
in the CLI is not discoverable. Every surface resolves to the same deterministic
backend operation.

### 2.2 Orchestrator — BEAM control plane

The top-level control plane. It owns:

- the **single** run lifecycle state machine (`05_LAYERING` §2.1; the Zig
  `orchestrator.zig` lifecycle is demoted to an embedded controller for the
  no-BEAM case),
- policy *decisions* (Tier-1 launch config + Tier-3 discretionary —
  `05_LAYERING` §4),
- the approval controller (human / UI / watcher routing),
- the canonical trace writer,
- provider *selection* (names a provider + config + transport preference order;
  never an implementation),
- scheduling, concurrency, budgets, session registry.

The orchestrator executes an **explicit** run state machine, not free-form model
continuation. It may use an agent to *propose* a plan (§4), but it runs a
durable, inspectable representation.

### 2.3 Agent runtime — the mitos engine (Zig)

This is the subsystem earlier specs called "mitos, the transport layer." It is
the stochastic-execution layer. It starts/connects an agent, sends input,
receives events, handles tool calls, and terminates or resumes a run.

It owns:

- the transport registry + implementations,
- the provider registry (argv construction, codec, prompt detectors — *all*
  provider-specific knowledge),
- the terminal core (libghostty-vt) as **one codec**, not the spine,
- sandbox invocation (hands Nucleus the compiled launch config),
- synchronous policy *enforcement* (Tier-2) + raw byte spool + first-pass
  redaction.

Transports are pluggable via three orthogonal axes (`05_LAYERING` §3):

```text
Transport (byte movement):  stdio | pty | socket(json-rpc) | http-stream | cloud-job
Codec (interpretation):     text | json-stream | acp | mcp | vt-screen
Lifecycle:                  one-shot | durable-session
```

The orchestrator's mental model of the engine: *"I hand you a LaunchSpec and a
compiled policy; you give me back a typed event stream and accept control
commands."* It does not know whether that is a PTY, a JSON stream, or a cloud
job. **PTY is a compatibility transport, not the default abstraction.**

### 2.4 Tool and capability plane

Tools are effect boundaries — how a non-deterministic agent changes the world.
They should be typed and permissioned:

```text
artifact.write   project.read   project.write   tests.run
git.push         package.install  query.database  publish.release
```

Raw shell access remains an **escape hatch**, not the default. Common effects are
promoted into typed tools for auditability and policy. Each tool call carries a
name, input/output schema, capability requirements, timeout, retry policy, audit
record, and owner subsystem. Discretionary tools are gated by Tier-3 policy.

### 2.5 Callback / authority plane

Agents call back into mitos through a typed channel, not by being trusted.

**Prompt = affordance, env = authority.** The system prompt tells the agent what
it *may* do; injected env makes the callback actually work for subprocesses,
scripts, retries, and remote workers:

```text
OD_DAEMON_URL   unix-domain socket mounted into the sandbox (network: none stays intact)
OD_PROJECT_ID
OD_RUN_ID
OD_TOKEN        run-scoped capability
```

The plane exposes an MCP surface over that socket: `tool_broker`, `approval`,
`artifact_register`, `memory_fetch`, `subrun_spawn` (`05_LAYERING` §6). This
turns a screen-scraped `ACTION_REQUESTED` into a *typed* MCP call, and is the
direct mitigation for the terminal-parsing fragility risk (`01_PRODUCT` §15.1).
`OD_TOKEN` scope maps onto the same Tier-3 policy the orchestrator already
evaluates — one policy engine, two entry points.

### 2.6 Security / enforcement — Nucleus

Mitos separates **instruction** from **authority**: an agent can be instructed to
call a tool, but only mitos grants the capability, and only Nucleus enforces the
launch boundary.

- Nucleus (gVisor strict-agent) enforces Tier-1 launch authority: network mode,
  filesystem mounts and rw boundaries, resource limits, env allow/deny,
  credential mounts (`03_NUCLEUS_INTEGRATION`).
- The terminal **observes**; it never enforces.
- Three-tier policy split (`05_LAYERING` §4): launch (Nucleus), synchronous
  invariants (engine), discretional (orchestrator).
- Scoped run tokens, approval gates, immutable audit log, budgets, and
  revocation on run end.

The agent is never the sole authority on whether an action is safe.

### 2.7 Memory and context

Memory belongs to the OS, not an unbounded prompt. Mitos owns storage,
retrieval, summarization, ranking, user-editable trees, source attribution,
forgetting, and privacy boundaries. Agents consume selected slices and may
*propose* updates; mitos validates, persists, and exposes them. Surfaced via
`memory_fetch` on the authority plane (§2.5).

> Status note: the memory subsystem is **nascent** in the current codebase. This
> section is target shape, not implemented state.

### 2.8 Artifacts and event log

Mitos persists outputs as artifacts and every meaningful occurrence as an event.

- The **orchestrator** is the canonical trace writer (`events.jsonl`, `run.yaml`,
  report) — centralizing audit for server / multi-run / durability.
- The **engine** writes only the high-volume raw spool (`terminal.raw`) and emits
  `RAW_REF` offsets; first-pass redaction happens at the engine boundary so raw
  secrets never cross the seam (`05_LAYERING` §7).
- Artifacts: diffs, `final.md`, `files_changed`, generated media, exports
  (`01_PRODUCT` §10).

The event log is the basis for debugging, replay, analytics, billing,
compliance, and recovery.

---

## 3. Run lifecycle

```text
1.  Receive CLI / API / scheduler request at an entry surface.
2.  Orchestrator resolves project, permissions, memory, and run plan.
3.  Compile policy: Tier-1 launch config + Tier-2 fast-path patterns.
4.  Grant a run-scoped capability (OD_TOKEN); start the authority plane socket.
5.  Engine resolves the highest-preference available transport and opens it
    inside the Nucleus sandbox.
6.  Stream typed engine events into the orchestrator's event log.
7.  Execute approved tool calls: Tier-2 auto-allow locally; Tier-3 brokered
    through the authority plane / orchestrator.
8.  Persist artifacts as they appear.
9.  Advance the run state machine.
10. Retry, branch, request approval, or finish.
11. Revoke run-scoped capabilities; checkpoint durable state.
```

A run record carries at least `id`, `project_id`, `workflow/state`, the selected
provider + transport + model, status, budgets, granted capabilities, and
timestamps (`01_PRODUCT` §10.1).

---

## 4. Planner agents and JIT workflows

Mitos may use an agent to generate the run plan itself, but **planning and
execution are different phases**:

```text
planner agent -> emits run plan / state machine
              -> deterministic validator
              -> orchestrator executes the explicit machine
```

The planner can be non-deterministic; the plan artifact must be explicit. This
prevents long-running agent drift — the model implicitly changing the task,
forgetting constraints, or inventing state transitions no durable system can
inspect.

---

## 5. Determinism boundary

The most important design boundary:

```text
Agent decides what to propose.
Mitos decides what is allowed.
Mitos records what happened.
Mitos advances durable state.
```

The test: *if the process crashes after an agent emits a tool call but before the
effect is committed, can mitos recover without guessing?* If not, the
architecture is too prompt-driven and not state-driven enough. This is the
motivation for durable run state and resume (`05_LAYERING` Phase 7).

---

## 6. Design principles

1. Keep deterministic control outside the model.
2. Treat agents as execution workers, not as the whole application.
3. Make the run lifecycle explicit, durable, and singular — in the orchestrator.
4. Give agents typed tools before raw shell access.
5. Use scoped capability grants (`OD_TOKEN`) instead of global authority.
6. Store memory in the OS and inject only relevant slices.
7. Record every meaningful event; the trace is the source of truth.
8. Support multiple agent transports; the orchestrator never names one.
9. Prefer structured stdio/JSON protocols; **the terminal is one codec, not the
   spine. PTY is compatibility, not the default.**
10. Keep local and cloud agents behind the same engine contract.
11. Separate prompt affordance from machine authority (`env = authority`).
12. Nucleus enforces; the terminal observes.
13. Make UI, CLI, API, and automation call the same backend operations.

---

## 7. Subsystem map → code

This replaces the generic `apps/ packages/ tools/` tree from the source
architecture with the actual mitos layout:

| Layer (this doc) | Lives in |
|---|---|
| Entry surfaces | `beam/` web/WebSocket UI; Zig CLI (`mitos`/`atr`) |
| Orchestrator (§2.2) | `beam/` — control-plane OTP app (lifecycle, policy decisions, approvals, trace writer) |
| Mitos engine (§2.3) | `src/` — transports, codecs, provider registry, terminal core, sandbox invocation, raw spool |
| Tool & authority plane (§2.4–2.5) | `beam/` authority-plane service + MCP over unix socket |
| Enforcement (§2.6) | Nucleus (`/media/data/proj/nucleus`) — external, behind the §`03` contract |
| Memory (§2.7) | target subsystem — not yet built |
| Trace & artifacts (§2.8) | orchestrator trace writer + engine raw spool |

The daemon (orchestrator + engine + authority + trace, with Nucleus as the
enforced boundary) is the authority-bearing program. The UI is a client; the CLI
is a client and a launch surface; agents are workers.

---

## 8. Relationship to the other specs

| Spec | Role under this constitution |
|---|---|
| `00_ARCHITECTURE` (this) | The constitution: mitos = the AI-native OS. Recontextualizes the name. |
| `01_PRODUCT` | Product thesis, target users, policy model, run artifacts, MVP scope. |
| `02_HLD` | Component-level high-level design. |
| `03_NUCLEUS_INTEGRATION` | The enforcement (sandbox) contract — §2.6. |
| `04_IMPLEMENTATION_PLAN` | Original Zig workstreams (pre-layering). |
| `05_LAYERING` | The orchestrator/engine seam — now an internal subsystem boundary of this OS, not the definition of mitos. |

This document sits *above* them. Where `05_LAYERING` scoped "mitos" to the Zig
engine, this document is the authority for the name.

---

## 9. Short summary

An AI-native operating system is not a prompt wrapped in a terminal. It is a
deterministic runtime that delegates ambiguous work to agents while retaining
control over state, authority, tools, memory, and artifacts.

The agent is the stochastic executor.

**Mitos is the operating system.**
