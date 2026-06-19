# 09 — Implementation Plan

Status: draft v0.2 · Date: 2026-06-18

How to build the `01_ARCHITECTURE` substrate on the existing BEAM+Zig codebase.
This doc is sequencing and reuse, not contract: field names and signatures are
owned by `01` §8 and elaborated in `03`–`08`. Three through-lines govern every
decision below:

1. **BEAM owns control; Zig is a local engine.** Per `01` §7, all orchestration,
   the log, the broker, the work pool, memory, tenancy, and remote-provider clients
   live on BEAM. Zig keeps only local transports, the terminal codec, the raw
   spool, and local sandbox launch. The two half-built control planes collapse to
   one — on BEAM.
2. **The log is built first.** State is `fold(events)` (`01` §4); replay, audit,
   billing, and the proof page are all reads of it. Nothing downstream is real
   until Decision/Effect append-and-replay works.
3. **The MVP is a vertical slice, not a layer.** We cut one thin path through
   every phase to reach the product gate, then widen.

---

## 1. Reuse map

Verdicts: **REUSE** (keep ~as-is) · **EVOLVE** (keep core, re-spec semantics) ·
**DEMOTE** (kept, no longer the primary path) · **DISCARD**.

### Zig engine (`src/`)

| Asset | Verdict | Becomes |
|---|---|---|
| `transport/` (stdio, pty, socket-jsonrpc, http-stream; codecs text/json/acp/mcp/vt) | REUSE | local transports under `05`; harness stdout/JSON + PTY codecs |
| `terminal/` (core, ghostty — libghostty-vt) | REUSE | PTY **fallback** codec only (`01` §8.6) |
| `pty/` (core, unix, windows) | REUSE | local PTY transport |
| `redaction/redactor.zig` | REUSE | first-pass redaction on the raw spool at the engine |
| `sandbox/nucleus.zig` (1288 L), `sandbox/backend.zig` | EVOLVE | **one** `SandboxProvider` (Nucleus/gVisor), driven from BEAM via the sidecar |
| `workspace/worktree.zig` | REUSE | hermetic workspace injection at `prepare` (`01` §8.5) |
| `providers/{claude,codex,gemini,fake,headless}.zig`, `launch.zig`, `catalog.zig` | EVOLVE | per-harness **adapters**: argv/env construction + structured event normalization (`01` §8.6) |
| `adapters/{registry,types,events}.zig` | EVOLVE | harness adapter registry + normalized event mapping |
| `adapters/detection.zig`, `prompt/` (detectors/providers/types) | DEMOTE | screen-scrape prompt detection → PTY-fallback path only |
| `providers/claude_log.zig` (539 L, session-JSONL tailer) | DISCARD | superseded by per-harness **structured observation** in the adapter (`01` §8.6) |
| `steer/` (keymaps, types) | REUSE | `control(session, command)` realization for PTY harnesses |
| `sidecar/` (main, protocol `{packet,4}`, msgpack) | REUSE | the engine⇄BEAM boundary; carries normalized events up, control down |
| `trace/` (file_store, types, artifact names) | EVOLVE | artifact naming + spool refs kept; the **canonical** Decision/Effect log moves to BEAM |
| `report/generator.zig` | REUSE | material for the run report / `08` proof page (later phase) |
| `policy/` (engine, types), `approvals/store.zig` | DEMOTE→DISCARD | policy compile + approvals move to BEAM; Zig copies retired once parity lands |
| `runtime/orchestrator.zig` (1617 L, `RunLifecycle` FSM), `runtime/types.zig` | **DISCARD** | the Zig control plane — deleted; control is BEAM-only |
| `main.zig` (CLI) | EVOLVE | thin local driver / harness-detector CLI; not a control plane |

### BEAM control plane (`beam/lib/`)

| Asset | Verdict | Becomes |
|---|---|---|
| `Mitos.Application` (supervision tree) | EVOLVE | add log writer, orchestrator host, broker, scheduler, registries |
| `Mitos.Orchestrator` (DynamicSupervisor of sessions) | EVOLVE | **rename `SessionSupervisor`**; it never was the agent — the orchestrator agent host is NEW |
| `Mitos.Session` (929 L, **PTY/sidecar hardcoded**) | EVOLVE | generalize behind `Mitos.SandboxProvider`; today's logic becomes the **local/Nucleus** provider session |
| `Mitos.Agent` (provider/task/transport entry) | EVOLVE | capability-launch entry; speaks `launch_spec`, not provider+task |
| `Mitos.Authority` (unix-socket callback plane: tool_broker/approval/artifact/memory/subrun) | EVOLVE | the **egress credential broker** (`01` §8.5). Reuse socket+token machinery; **invert** semantics: deny-all-except-broker, secrets only at broker, not agent-initiated callback |
| `Mitos.Authority.{Request,Verdict}` | REUSE | broker request/verdict structs |
| `Mitos.Trace` (events.jsonl writer + artifact names) | EVOLVE | the **append-only event-log writer** (Decision+Effect, `01` §8.1/8.2) |
| `Mitos.Runs` (read side) | EVOLVE | run-viewer read model = `fold(events)` |
| `Mitos.PubSub`, `Mitos.Web.{Endpoint,Router,TerminalSocket}` | REUSE | live run-viewer fan-out + WS |
| `Mitos.Sidecar.Protocol` (`{packet,4}` decode) | REUSE | engine boundary on the BEAM side |
| `Mitos.Workload` + `Workload.Runner` (seller-packaged JSON process, `/workloads/:id/execute`) | DEMOTE | a pre-capability stand-in; **superseded** by the capability manifest (`01` §8.3) but reused wholesale for the MVP slice |

**Net deletions:** `src/runtime/orchestrator.zig` and `src/providers/claude_log.zig`
are the two largest discards and the clearest signal that the redesign landed —
the Zig control plane and the screen/log scraper both go.

---

## 2. Phased plan

P0 is a de-risking pre-phase; P1–P7 follow `01`'s layer order. Each phase lists
its hard dependency and exit criterion.

| Phase | Scope | Depends on | Exit criterion |
|---|---|---|---|
| **P0 Collapse** | delete `runtime/orchestrator.zig`; rename `Orchestrator`→`SessionSupervisor`; stub `Mitos.SandboxProvider` behaviour; one supervision tree | — | builds green; one provider behaviour, one control plane |
| **P1 Control-plane core** | append-only event log (Decision/Effect), orchestrator agent host, loop-manager FSM, resume/replay | P0 | crash-test (`01` §4) passes; `fold(events)`=state; replay→re-invoke |
| **P2 Compute-plane local** | `SandboxProvider` behaviour live; Nucleus + Docker providers; egress credential broker; hermeticity; harvest | P1 | a capability runs in Nucleus, deny-all egress, secret brokered, diff+result harvested |
| **P3 Capability model** | manifest, image build, RO DATA mount, harness adapter contract (`01` §8.6), deterministic hooks, soft result+evidence | P2 | manifest→image→run→`{result, evidence}`; ≥2 harnesses injectable |
| **P4 Remote providers** | E2B + Daytona provider clients (HTTP/WS); broker-only egress for **T2**; RO data snapshots | P2, P3 | untrusted capability on buyer data in remote µVM; deny-all-else holds |
| **P5 Work Pool** | pools + disciplines + Task dependency graph + invoke/subtasks + service runtime (kept) + composition (targets/presets/conflicts/derived view) + triggers | P1, P3 | a service triggers a pipeline; deps + conflicts resolved deterministically; an invoke returns a subtask result |
| **P6 Memory + self-improvement** | two-track knowledge service; contradiction-supersession forgetting; verify→distill loop | P1, P3 | memory written via Effect; retrieval injected; verified runs distill |
| **P7 Facilitator** | multi-tenancy/orgs/keys; key custody at broker; metering off the log; x402/USDC settlement; proof pages | P3, P4, P7-tenancy | a stranger pays; capability fulfills; metered+settled+proof |

### Ordering rationale

- **P0 before everything.** Two control planes and a PTY-locked session are active
  contradictions with `01`. Collapsing first removes the temptation to extend the
  wrong plane and gives one seam (`SandboxProvider`) for P2–P4 to fill.
- **P1 first among features.** Per `01` §4 the log *is* the state; resume, audit,
  metering (`08`), and the proof page are all reads of it. Building the broker,
  scheduler, or memory before the log would mean writing their state twice. The
  crash-test is the gate: it proves Decision-then-Effect recovery before any
  effect is expensive.
- **P2 before P3.** The `SandboxProvider` boundary and broker are the security
  model (`01` §3). Defining the manifest before there is a sandbox to honor it
  risks a manifest that the boundary can't enforce. Build the enforcement edge,
  then the package that flows through it.
- **P4 after P3, parallel to P5.** Remote providers are *another implementation*
  of the same `SandboxProvider` behaviour — cheap once the contract is proven
  locally, and the only thing that unlocks true T2/open seller onboarding.
- **P6 leans on P1+P3, not on remote.** Memory writes are Effect events; the
  verify→distill loop consumes capability `result+evidence`. It needs the log and
  the capability, nothing in the compute plane beyond P2.
- **P7 last** because it *composes* the rest: it meters the P1 log, custodies keys
  at the P2 broker, isolates untrusted sellers with P4, and packages P3
  capabilities. Its only net-new substrate is tenancy, settlement, and proof
  rendering.

### Dependency diagram

```
                         ┌──────────────── cross-cutting (threads every phase) ────────────────┐
                         │  auth/tenancy · run viewer (web/WS) · observability/telemetry        │
                         └─────────────────────────────────────────────────────────────────────┘

 P0 Collapse
   (1 control plane, SandboxProvider seam)
        │
        ▼
 P1 Control-plane core  ── event log · orchestrator host · loop-manager FSM · resume/replay
        │          \
        ▼           \________________________________
 P2 Compute local                                     \
   SandboxProvider · Nucleus+Docker · broker · harvest  \
        │                                                ▼
        ▼                                          P6 Memory + self-improve
 P3 Capability model ── manifest · image · RO data        (verify→distill)
   harness adapter · hooks · result+evidence
        │           \
        ▼            \________________
 P4 Remote (E2B/Daytona, T2)          \
        │                              ▼
        │                         P5 Work Pool (pools/disciplines · deps · invoke · services · targets)
        ▼
 P7 Facilitator ── tenancy/keys · broker custody · metering(log) · x402/USDC · proof pages
   (needs P3 capability + P4 isolation + P1 log + P2 broker)
```

---

## 3. The MVP cut

**Success gate (`02`/`08`):** *one stranger-paid, machine-fulfilled transaction
with proof.* The smallest slice that reaches it touches five phases thinly and
skips three entirely.

**In (thin):**

- **P0** — full. Non-negotiable; nothing builds cleanly otherwise.
- **P1 (subset)** — append-only Decision/Effect log + `fold`→state + the
  **crash-test**. A **single linear pipeline** (one or two capabilities), no
  insert/switch/skip branching. Loop-manager runs **one iteration, no escalation
  model call**. Full resume/replay deferred (crash-safety kept; mid-run replay
  not required for a single short job).
- **P2 (subset)** — `SandboxProvider` behaviour + **Nucleus provider only**.
  Broker in its **withhold-or-attach-one-credential** form with deny-all-else
  egress and Effect logging. Harvest = workspace `diff` + `RESULT` artifact.
  Docker provider deferred.
- **P3 (subset)** — capability manifest (`01` §8.3) + **one harness adapter
  (Claude Code, structured)** + post-hook `git diff` + soft `result+evidence`.
  Image may be **pre-baked** (no build pipeline yet); RO DATA mount optional.
- **P7 (subset)** — two-party identity (buyer/seller), **metering off the log**,
  **x402/USDC settlement**, **proof page** rendered from the event log + harvested
  evidence. Reuse `Mitos.Workload` + `/workloads/:id/execute` as the literal
  request surface, swapping its internals onto P1–P3.

**Out of MVP:** P4 remote/T2 µVM, P5 work pool (deps/disciplines/invoke/services), P6 memory/self-improvement,
multi-step branching pipelines, full multi-tenant org model, image build pipeline,
second+ harness, snapshot-based mid-run resume.

**The load-bearing judgment call:** MVP runs seller capabilities at **T1 in local
Nucleus on an operator-vetted basis** — i.e. we onboard a *known* seller, not the
open public. True **T2 untrusted-seller isolation requires P4** and MUST land
before open seller onboarding. The gate ("a stranger *pays*") is met by an
arbitrary *buyer*; the *seller* stays curated until P4. Flag for sign-off.

---

## 4. Cross-cutting concerns

These thread every phase; build their seams in P0/P1, fill incrementally.

- **Auth / tenancy.** Introduce an `org_id`/`actor_id` on the `run_id` namespace
  in P1 (cheap, single-tenant default) so the log is tenant-stamped from day one;
  the full org/key/role model is P7. Retrofitting tenancy after the log exists is
  a migration; stamping it at creation is free. Keys live **only at the broker**
  (P2) — never in env, never in the log (only key *references* and brokered-crossing
  Effects).
- **Web/WS run viewer.** Reuse `Mitos.Web.*` + `PubSub` + `TerminalSocket`. Repoint
  the viewer's read model from the old protocol-event trace to `fold(events)`
  (`Mitos.Runs`, P1). The same WS that streams PTY bytes today streams normalized
  harness events (`01` §8.6); PTY view becomes the fallback tab. UI, CLI, API, and
  automation MUST call the same backend operations (`01` §10.11).
- **Observability.** The event log *is* the primary trace; add `:telemetry`
  spans around Decision→Effect commit, broker crossings, and provider
  `prepare/start/harvest` so latency/cost are queryable without a second store.
  Metering (P7) reads the same `budget_charged` Effects.

---

## 5. Risks

| Risk | Phase | Mitigation |
|---|---|---|
| Event-log schema churn after downstream depends on it | P1 | freeze `01` §8.1/8.2 field names before P2; version the log; treat additions as append-only |
| Harness structured-output drift (CLIs change formats per release) | P3 | adapter owns normalization (`01` §8.6); PTY+vt codec is the always-available fallback; pin harness versions per capability image |
| Inverting `Mitos.Authority` from agent-callback to broker-mediated egress is a semantic reversal, not a rename | P2 | re-spec semantics first (deny-all-except-broker); reuse only socket/token plumbing; keep MCP demoted (`01` §8.5) |
| `Mitos.Session` is 929 L of PTY assumptions | P0/P2 | extract the behaviour seam in P0 before adding the Nucleus path; let the PTY path be the first `SandboxProvider` impl, not a special case |
| T2 isolation correctness (untrusted seller on buyer data) | P4 | gate open seller onboarding on P4; MVP curates sellers (§3) |
| Two trace writers (Zig file_store + BEAM log) double-write | P1 | BEAM log is canonical; Zig keeps only raw spool + refs; retire Zig trace control fields |
| Settlement/key-custody handling real funds | P7 | keys at broker only; metering is a pure read of the log → auditable; x402 settlement reconciles against `budget_charged` Effects |

**Sequencing summary:** collapse to one plane → make the log real and crash-safe →
build the sandbox boundary and broker → package the capability that flows through
it → then widen (remote, work pool, memory) and monetize (facilitator). The MVP
borrows one thin thread from P1/P2/P3/P7 and the existing `Workload` surface to
reach the product gate before any of those phases are complete.
