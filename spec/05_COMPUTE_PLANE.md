# 05 – Compute Plane

This document specifies **how a capability runs safely** – locally and remotely –
behind one provider abstraction, and how mitos enforces authority at the **sandbox
boundary**. It elaborates `01_ARCHITECTURE` §6 (trust tiers), §7 (runtime
ownership), §8.4 (`SandboxProvider`), §8.5 (authority boundary), and §8.6 (harness
adapter). Where this doc and `01` disagree, `01` wins; field names from `01` §8 are
reproduced verbatim and MUST NOT be renamed.

Two corrections vs earlier mitos thinking are load-bearing here. **(1) Nucleus is
one provider, not the architecture** – the gVisor strict-agent sandbox (Nucleus) is an **external product**, shipped as OCI containers; mitos integrates with it as one `SandboxProvider` implementation, described by contract, not by its internals. **(2) The authority plane is the sandbox
boundary, not an in-harness MCP server** – authority is granted by *inbound
hermetic injection*, gated by an *outbound egress credential broker*, and results
come from *harvest*; MCP is a DEMOTED, optional tool affordance (§5.5), never the
trust boundary.

RFC 2119 keywords (MUST / SHOULD / MAY) are normative. Every provider – Nucleus, Docker, E2B, Daytona – is an external product behind the `SandboxProvider` seam (`01` §0); mitos ships only the in-process `Provider.Local`.

---

## 1. The standard usage model – clone the environment *into* the sandbox

The whole environment is assembled **inside** the sandbox; the harness then runs
**natively** there, as it would on a developer laptop. Mitos sits outside: it
**observes the harness process** (normalized event stream, §5.2) and **controls the
boundary** (inject / broker / harvest). It does not reach in to mediate individual
tool calls.

```
                    SANDBOX  (isolation = security boundary)
        ┌───────────────────────────────────────────────────────┐
   ①    │  workspace/ (input repo cloned IN, RW) · /cap (image,  │
 inject │  RO) · /data (RO snapshot) · /ctx (context, RO)        │
 ─────► │   ┌─────────┐  normalized events ──② observe──►        │
        │   │ harness │  (runs NATIVELY: Claude Code/Codex/…)     │
        │   │ process │ ◄──── steer: control(cmd) ──②             │
        │   └────┬────┘  any network syscall                      │
        │        ▼                                                │
        │   deny-all-except-broker ────────③ egress────►  BROKER  │
        └───────────────────────────────────────────────────────┘
                                   harvest ④ ──► diff + RESULT + artifacts
```

This is the same shape used by E2B, Daytona, and Codex-cloud: ship the repo into a
disposable VM/container, let the agent work, snapshot/diff out the result.
Codex-cloud additionally **disables or allowlists network during the task** – that
network cutoff *is* the exfiltration defense. Mitos generalizes it: every provider
MUST route egress through the broker (§5.3). Hence a hard admission requirement:

> **R1 (provider admission).** A remote provider MUST be able to force *all*
> sandbox egress through a mitos-controlled broker endpoint (forced proxy or
> network policy), and MUST keep the workspace/data filesystem inside the isolation
> boundary. A provider that cannot do both is ineligible for T2.

The capability's `harnesses` set (`01` §8.3) names the compatible harnesses; mitos
**injects** one at launch (`03_CAPABILITY`, `01` §5) and can switch it mid-step
(loop-manager `switch`, `04_CONTROL_PLANE`). The harness adapter (`01` §8.6)
normalizes observation; the sandbox provider normalizes execution.

---

## 2. The `SandboxProvider` behaviour

One behaviour, many backends. Reproduced from `01` §8.4, then elaborated:

```elixir
@behaviour Mitos.SandboxProvider

prepare(launch_spec)      :: {:ok, handle} | {:error, reason}
start(handle, argv, env)  :: {:ok, session} | {:error, reason}
stream(session)           :: Enumerable.t(event)     # NORMALIZED harness events (§8.6)
control(session, command) :: :ok | {:error, reason}  # steer: approve/answer/interrupt/select/prompt
snapshot(session)         :: {:ok, snap} | :unsupported
harvest(session)          :: {:ok, %{diff: _, result: _, artifacts: _}}
stop(session)             :: :ok
capabilities()            :: %{isolation: _, remote?: _, egress: :broker,
                               data_mount: _, snapshot?: _, net_modes: _}
```

`launch_spec` is compiled from the capability manifest (`01` §8.3) + `trust_tier`
+ run context by the **launch policy compiler** (§4). It carries no secrets:

```
LaunchSpec {
  run_id, capability_ref, trust_tier : T0|T1|T2
  image       : <oci-ref>                       # SKILL+TOOLS, content-addressed
  workspace   : { source: <git-ref|tarball>, mode: rw }      # input, cloned IN at prepare
  data        : { snapshot: <ref>, mount: "/data", ro: true }
  context     : [ <ctx-slice-ref> ]             # curated memory/context (07), RO
  harness     : claude-code | codex | ...        # the injected one
  posture     : { net_mode, mounts, broker_endpoint, ca_ref, resources, timeouts }
  egress_policy: [ {dest, credential_id, requires_approval} ]  # IDs only – no secret material
}
```

| Op | Contract | Inputs | Guarantees |
|---|---|---|---|
| **prepare** | Materialize a runnable, hermetic sandbox: clone workspace IN, mount image RO, attach RO DATA snapshot, drop RO context slices, set `net_mode=deny-all-except-broker`, install the mitos CA for brokered TLS, neutralize harness HOME/config (§7). **No secrets in env.** | `launch_spec` | Returns an opaque `handle`. Idempotent on `run_id`. MUST NOT start the harness. MUST fail closed if egress cannot be wired to the broker. |
| **start** | Launch the harness process inside the prepared sandbox with `argv`/`env`. | `handle, argv, env` (env = non-secret config only) | Returns a `session`. Process is the *only* writer of the workspace. |
| **stream** | Yield the **normalized** harness event stream (§8.6): `assistant_text \| tool_use \| tool_result \| token_usage \| status \| final_result`. | `session` | Ordered, at-least-once with monotonic offsets; resumable from last offset on reconnect. This is the loop-manager's input. |
| **control** | Steer the live session: `approve \| answer \| interrupt \| select \| prompt`. **mitos-initiated**, never an agent callback. | `session, command` | Best-effort delivery; ack or `{:error,…}`. Ordering preserved per session. |
| **snapshot** | Capture resumable state (filesystem + harness session if reusable). | `session` | `{:ok, snap}` or `:unsupported`. `snap` is opaque + content-addressed; restore via `prepare(launch_spec{from: snap})`. |
| **harvest** | Extract results without trusting the agent: workspace **diff**, the `RESULT` artifact, and **registered** artifacts. | `session` | `{:ok, %{diff, result, artifacts}}`. Pure read; MUST be callable after a clean *or* failed/timed-out stop. |
| **stop** | Terminate the session and release resources. | `session` | Idempotent. Local: kill process tree + tear down namespaces. Remote: destroy VM/workspace. |
| **capabilities** | Static descriptor for the policy compiler. | – | `egress` MUST be `:broker`. `net_modes`, `data_mount`, `snapshot?`, `remote?`, `isolation` drive eligibility (§4). |

Lifecycle: `prepare → start → (stream ⇄ control)* → [snapshot] → harvest → stop`.
The `commit`/`capability_finished` Effect (`01` §8.2) is emitted off `harvest`, not
by the harness.

---

## 3. Provider matrix

Each row is a `SandboxProvider` implementation. Columns map directly to
`capabilities()` and `01` §6.

| Provider | Isolation mechanism | Locality | Egress / authority transport | DATA mount | Snapshot | Default tier |
|---|---|---|---|---|---|---|
| **Nucleus** (gVisor strict-agent) | gVisor userspace kernel + seccomp; per-run agent; syscall interposition | **local** | gVisor netstack `deny-all`; **unix-domain socket** to the in-process broker | bind (RO/RW) or RO overlay | yes – gVisor checkpoint/restore | **T0 / T1** |
| **Docker / OCI** | namespaces + cgroups + seccomp/AppArmor (shared host kernel – **weak**) | **local** | host-loopback forced proxy + docker network policy (allowlist = broker only) | bind (RO/RW) | coarse (commit only) – treated as **no** | **T1** |
| **E2B** | Firecracker **microVM** (KVM, separate kernel) | **remote** | **forced HTTP/SOCKS proxy** → broker over TLS; VM firewall deny-all-else | RO provider **volume / snapshot** | yes – Firecracker VM snapshot | **T2** |
| **Daytona** | workspace VM / container | **remote** | network policy → broker proxy; egress allowlist = broker endpoint only | RO provider volume | partial – workspace snapshot | **T2** |

- **Docker is weak isolation** (shared kernel): acceptable for T1 and dev, but MUST
  NOT be selected for T2. **Nucleus** is the strong *local* option – gVisor's
  userspace kernel narrows the host attack surface and the broker is reachable only
  over a unix-domain socket, so deny-all-except-broker lives in the netstack.
  **E2B / Daytona** are the strong *remote* options – a separate guest kernel
  defends escape; forced-proxy egress defends exfiltration. New providers plug in
  by satisfying R1 + the behaviour; `04`'s orchestrator/loop-manager are
  provider-agnostic.

Per `01` §7, **ownership splits by locality**: the **Zig engine** owns local
providers (Nucleus, Docker), local transports (pty/stdio), and in-band synchronous
enforcement for local runs; the **BEAM control plane** owns the **remote** provider
clients (E2B/Daytona over HTTP/WS), their event streams, and the **egress
credential broker** for all tiers.

---

## 4. Trust tiers → provider + posture (the launch policy compiler)

`trust_tier` is an input; the deterministic **Tier-1 launch policy compiler** (BEAM,
coded not agentic – `01` §3's enforcement layer) maps it to an **eligible provider
set** + **network/mount posture**:

```
compile(manifest, trust_tier, run_ctx) ->
    eligible = providers where capabilities() ⊇ required(tier)
    select   = pick(eligible, run_ctx.locality_pref, cost, availability)
    => LaunchSpec{ provider: select, posture: posture_for(tier),
                   egress_policy: manifest.egress_policy }
```

| Tier (`01` §6) | Who | Eligible providers | Network posture | Mount posture | Metering |
|---|---|---|---|---|---|
| **T0** | first-party/system caps (orchestrator helpers, verifiers) | Nucleus (Docker dev-only) | broker via unix socket; deny-all-else | bind, RO/RW | internal |
| **T1** | local user on own code | Nucleus / Docker | local broker; deny-all-else | bind (RW workspace, RO data) | internal |
| **T2** | **untrusted tenant/seller capability on buyer data** | **E2B / Daytona only** | **broker-only egress, deny-all-else, no host network** | **RO data volume/snapshot; NO host bind mounts; ephemeral RW workspace only** | **metered → `08`** |

T2 hard constraints (the policy compiler MUST refuse to emit a `LaunchSpec` that
violates any):

1. **remote microVM** isolation (`remote? = true`, microVM-class `isolation`);
2. **broker-only egress**, deny-all-else, no host-network/loopback bypass;
3. **RO data** as a provider snapshot/volume – never a host bind mount;
4. **no host bind mounts** at all; workspace is an ephemeral in-guest clone;
5. **fully metered** (CPU/mem/wall/egress) into the billing ledger (`08`).

If no eligible provider exists for a tier (e.g., remote provider unavailable for a
T2 run), the compiler MUST fail the launch (emit nothing) rather than silently
downgrade isolation. **Distrust scales isolation; it never relaxes it.**

---

## 5. The authority boundary in depth

Three controlled crossings + harvest (numbered ①–④ in the §1 diagram). This is the
whole security model: trust the *boundary*, not the agent and not a protocol the
harness must speak. On the mitos side, the **secrets vault**, the **broker**, and
the **event log** (`01` §8.2) all sit *outside* the sandbox; only the broker ever
holds real credentials.

### 5.1 ① Inbound – hermetic injection (at `prepare`)

Everything the run is allowed to *have* enters here, and **nothing else does**:
the **workspace** (input repo **cloned INTO** the sandbox, RW ephemeral); the
**capability image** (SKILL+TOOLS, RO `/cap`); the **RO DATA snapshot** (RO
`/data`, §7, versioned with the capability); **curated context slices** (selected
by `07_MEMORY`, RO `/ctx`); and the **mitos CA** for brokered-TLS (§5.3).

**What "cloned in" means.** The clone MUST honour the source's ignore rules –
tracked files plus untracked-but-not-ignored ones. Build output, caches, VM
images, and fuzz corpora are not the subject of the work and were never meant to
cross the boundary; copying them wholesale is not an isolation guarantee but a
disk-exhaustion path (a 302 GB checkout copied per stage, before any model
spend). A clone that would exceed the configured ceiling MUST be **refused with
its largest contributors named**, never started and discovered at 94% disk.

**Turning the sandbox off (`mode: :in_place`).** A T0/T1 operator working on
their own repo MAY run without the copy: the workspace *is* their tree. This is
a per-run, explicitly-requested, **logged** choice (`run_started.workspace_mode`)
– never a default and never inferred – because it changes the authority story:
there is no copy to throw away, so an `internal` act is now an edit to the
operator's real files, and `16 §3.3`'s rewind becomes a git restore rather than
a tree swap.

Three obligations follow, and they are normative:

| In `:in_place`, mitos MUST NOT | because |
|---|---|
| `git init` / `git add -A` / `git commit` in the workspace | it is the operator's repository; a baseline or checkpoint that moves their branch or stages their tree is a side effect they never asked for. Use a non-mutating commit object (`git stash create`) pinned under `refs/mitos/`. |
| archive the tree per checkpoint | the copy is the thing being avoided; the pinned commit is the recovery path |
| delete untracked files on rewind | a rewind may undo the run's edits, not a file the operator made that git never knew about. An in-place rewind is **not a perfect inverse**, and says so. |

**T2 MUST refuse `:in_place` outright.** The sandbox boundary is the security
boundary (`01 §1.3`); distrust scales isolation and an operator convenience may
not relax it. The launch policy fails closed here exactly as it does for an
ineligible provider (§4).

**No secrets enter the sandbox.** `env` carries non-secret config only (model id,
effort, paths, flags). Any credential the capability needs is named by
`egress_policy` *by id* and materialized only at the broker (§5.3). Injecting a
token into `env` or a file is a spec violation, even at T0.

### 5.2 ② Observe / steer (mitos-initiated)

Mitos **drives** the harness process and **reads its normalized event stream**
(`01` §8.6): `assistant_text | tool_use | tool_result | token_usage | status |
final_result`. Steering goes the other way via `control(session, command)` –
`approve | answer | interrupt | select | prompt`. This is **mitos-initiated**, not
an agent callback: the agent never "calls home" for authority. Per `01` §7, the
**Zig engine** owns local transports (pty/stdio, VT parser, raw spool +
first-pass redaction) and the **BEAM control plane** owns remote provider streams
(HTTP/WS); both normalize via the harness adapter into one stream feeding the
loop-manager (`04`).

**The stream has two possible sources, and the boundary hides which** (`14 §1.1`).
For a headless/structured harness it is stdout. For an **unmodified interactive**
harness it is a tail of the harness's own session artifacts – which mitos can
read precisely because §6's hermeticity already redirects `HOME` and the session
store to mitos-controlled paths inside the sandbox. Structured observation of an
interactive session therefore needs no mode change and no screen parsing.

**The terminal is a third channel with one job.** Semantics never come from it
(`14 §1`); it answers only *what is this terminal waiting for*. Its three
operations, all owned by `14 §3`:

| Op | Cadence | Rule |
|---|---|---|
| `spool` | continuous | raw bytes → append-only redacted artifact; **never parsed for control**. This is evidence, and it is what makes the channel compose with proof and replay. |
| `snapshot` | **on demand only** | refuses unless the VT parser is at a consistent boundary *and* the session is at a quiescence edge ≥ `T_snap`. Returns a digested grid anchored to a spool offset – a pure function of the spool prefix, so any later replay or audit recomputes it. |
| `steer` | on a quiescence edge ≥ `T_steer` | a verified transaction: snapshot-before → write → snapshot-after → assert the classifier's predicted transition, else `steer_rejected`, send ESC, and escalate. Never a fire-and-forget keystroke. |

Continuous screen parsing remains banned (`01 §8.6`): the prior implementation's
failure was **streaming a display protocol**, and every error class it hit –
torn frames, split escape sequences, redraws read as new content – follows from
sampling a moving surface rather than reading a stable one. No run-control
decision may rest on a screen read alone; a Decision triggered by a
classification MUST carry the grid digest and spool offset in its `inputs_hash`
preimage. An `:unknown` classification escalates to a human (`15`), never
guesses.

### 5.3 ③ Outbound – egress credential broker (the only way out)

Sandbox egress is **deny-all-except-broker**. The broker is the single egress
chokepoint and the **only** place real credentials exist.

Per-request algorithm (each network attempt from the sandbox):

```
on request(dest, method, headers, body) from sandbox:
  rule = match(dest, egress_policy)                 # 01 §8.3 egress_policy
  case rule:
    none                      -> EMIT egress_denied; return 403            # default deny
    {requires_approval:true}  -> EMIT approval_requested; PARK
                                   on approve   -> attach+forward (below)
                                   on deny/timeout -> EMIT egress_denied; 403
    {credential_id:c}         -> cred   = vault.get(c)                     # secret fetched HERE
                                  outbound = attach(cred, dest, headers)   # Authorization/header/mTLS
                                  resp     = forward(outbound)
                                  EMIT egress_allowed{dest, bytes, status}
                                  return resp
```

Key properties:

- **Default deny.** No matching `egress_policy` rule ⇒ `egress_denied`.
- **Secrets attached at the edge.** The broker injects the real credential
  (bearer/header/basic/mTLS cert) keyed by `credential_id`+destination, then
  forwards; the sandbox only ever sees an *uncredentialed* request leave it. To
  attach a credential to HTTPS the broker terminates TLS with the mitos CA injected
  at `prepare` (trusted **only** for brokered destinations) – a deliberate, scoped
  MITM owned by mitos, the price of "no secrets in the sandbox."
- **Every crossing is an Effect event.** `egress_allowed` / `egress_denied`
  (`01` §8.2) with destination, bytes, status, `caused_by` – the log is both audit
  record and egress meter for `08`. `requires_approval` parks the request, emits
  `approval_requested`, and `approval_resolved` gates forward-vs-deny through the
  same control-plane UI/API as run approvals (`06`).

**Wiring egress to the broker.** *Remote (E2B/Daytona):* the provider forces all
egress through a mitos-controlled **HTTP/SOCKS proxy** (the broker's ingress) over
TLS and/or a VM **network policy** whose only allowed destination is that proxy;
`HTTP(S)_PROXY` is set in the guest and the firewall drops everything else, so the
proxy cannot be bypassed (realizing R1). *Local (Nucleus):* egress is broked
**in-process** – gVisor's netstack denies all outbound and the capability reaches
the broker over a **unix-domain socket**, so no TCP path to the host network exists
to bypass. (Docker maps the same idea onto a host-loopback proxy + docker network
policy allowlisting only the broker.)

### 5.4 ④ Harvest (results, not callbacks)

Results are **pulled** by mitos via `harvest`, never pushed by the agent: the
**workspace diff** (RW changes to the cloned-in repo); a **`RESULT` artifact** (the
capability's declared `result` = schema + evidence, `01` §8.3, at a known path);
and **registered artifacts** (each an `artifact_registered` Effect, `01` §8.2).
Harvest MUST succeed after a clean *or* failed/timed-out stop, so partial work and
evidence survive. The orchestrator interprets `result + evidence + trace` (`01` §5,
`04`); the harness never emits a control token.

### 5.5 MCP is demoted

For harnesses that speak MCP, mitos MAY expose an in-sandbox MCP relay as a **tool
affordance** (extra TOOLS), per `03_CAPABILITY`. It is **never** the authority
boundary: any MCP-driven network call still exits through the broker (§5.3), and
no MCP server grants a credential. MCP off ⇒ capability still fully governed.

---

## 6. Hermeticity

**Hermetic by default.** A run carries only what mitos injected (§5.1); the harness
MUST NOT pick up cross-run state from the host or a previous run. At `prepare`, the
harness adapter (`01` §8.6) **neutralizes or redirects** every native cross-run
surface to ephemeral, mitos-controlled paths inside the sandbox:

| Native surface | Hermetic action |
|---|---|
| `HOME` / dotfiles | redirect to ephemeral `HOME` under the sandbox |
| harness config (e.g. `~/.claude`, `~/.codex`) | neutralize; inject only mitos-provided config |
| session store / history | redirect to ephemeral path; discarded at `stop` |
| auto-loaded project memory (`CLAUDE.md`, agents files) | neutralized unless supplied as a `/ctx` slice (`07`) |
| ambient credentials (env, keychains, cloud metadata) | not present (no secrets injected; metadata endpoint blocked by deny-all) |

**Opt-in harness-native session reuse.** A capability MAY declare it wants the
harness's native session/memory to persist across iterations or runs (e.g. a
long-lived assistant). Mitos then mounts a **dedicated, per-capability** session
volume that `snapshot`/restore carries. Reuse is explicit and per-capability; it
never leaks across capabilities, tenants, or trust tiers. Memory mitos *manages*
(the `/ctx` track) is governed by `07_MEMORY`; this knob is only the harness's own
store.

---

## 7. DATA mounting

DATA is the capability's **persistent corpus/refs/DB**, distinct from the input
**workspace** (`01` §5), and versioned **with the capability** (the manifest pins
`data.snapshot`, `01` §8.3) so a version always sees the same data.

| Tier | DATA mount | Why |
|---|---|---|
| **T0 / T1** | local **bind** mount, RO (`/data`) | trusted, local, fast; no copy |
| **T2** | remote **RO provider snapshot/volume** | untrusted code on buyer data MUST NOT get a host bind; a provider volume keeps data inside the isolation boundary, preventing host-FS reach-through |

DATA is **read-only** in all tiers (RW persistence is a non-goal here; outputs flow
through harvest §5.4). Snapshots are content-addressed and pinned; a capability
upgrade needing new data ships a new `data.snapshot` ref, keeping runs reproducible
and replayable (`01` §4).

---

## 8. Snapshot / resume, timeouts, failure handling

> `snapshot`/`resume` here is **crash** recovery. `16 §3` layers **semantic**
> recovery on the same primitive: a checkpoint pairs this snapshot with a
> workspace commit, a log offset, and an acceptance-predicate verdict, so a run
> can rewind to the last *green* state rather than only to the last live one. A
> provider returning `:unsupported` still checkpoints on commit + log offset;
> rewind then replays from the commit.


**Snapshot / resume.** `snapshot(session)` captures filesystem (+ optional harness
session, §6) as an opaque content-addressed `snap`; resume is
`prepare(launch_spec{from: snap})`. Providers without real checkpointing return
`:unsupported`; mitos falls back to **event-log replay** (`01` §4) – re-`prepare` a
fresh sandbox, re-drive committed Decision/Effect events, re-invoke the
orchestrator. Filesystem snapshot is an *optimization*; the **event log is the
authoritative resume source**.

**Timeouts.** `resources.timeout` (`01` §8.3) bounds wall-clock; the policy
compiler also sets per-tier ceilings. On timeout: `interrupt` → `harvest` (partial
work survives) → `stop`. Parked egress approvals have their own timeout →
`egress_denied`.

**Failure handling.**

| Failure | Response |
|---|---|
| harness crash | `harvest` partial (§5.4) → loop-manager `retry`/`escalate` (`04`); idempotent on `run_id` |
| sandbox/provider unavailable | policy compiler re-selects an eligible provider (§4); **never** a lower tier |
| broker denies/parks egress | `egress_denied` Effect; capability sees a normal network error; orchestrator may `request_approval` |
| crash between Decision and Effect | re-drive the Decision (`01` §4 crash test) – Effect not committed ⇒ action simply re-run |
| timeout / budget exhausted | interrupt → harvest → stop; `budget_charged` to ledger (`08`) |

All transitions land as Effect events (`01` §8.2); the run is replayable and
billable from the log.

---

## 9. Threat model

Two primary threats, each defended by a *different* mechanism – neither relies on
trusting the agent or the model:

| Threat | Vector | Defense | Where |
|---|---|---|---|
| **Secret exfiltration** | malicious/compromised capability tries to ship a credential or buyer data to an attacker | **egress credential broker**: deny-all-except-broker, secrets never in the sandbox, default-deny + per-destination policy, approvals, every crossing logged | §5.3 |
| **Sandbox escape** | capability tries to break isolation to reach host/other tenants | **isolation scaled to distrust**: T2 ⇒ remote microVM (separate kernel) + RO provider data + no host bind mounts | §3, §4 |

Corollaries: prompt-injection that convinces the agent to exfiltrate is contained
by the broker (the agent *cannot* attach a credential or reach a non-policy
destination); a host-kernel 0-day is contained at T2 by the microVM boundary; and
since DATA is RO and outputs flow only through harvest, a capability cannot
silently mutate buyer data or smuggle results around the log. Residual risks
(acknowledged): the broker's TLS-interception CA is a trusted component (compromise
= credential exposure) and MUST be tightly held; covert channels within an
*allowed* destination are out of scope here, mitigated by `egress_policy` scoping +
byte metering, not eliminated.

---

---

## Cross-references

capability manifest, harness adapter, MCP-as-tool → `03_CAPABILITY`;
orchestrator/loop-manager, Decision/Effect schema, resume/replay → `04_CONTROL_PLANE`;
approval surfacing, services/triggers → `06_WORK_POOL`; `/ctx` context slices →
`07_MEMORY`; metering off the egress/effect log, multi-tenancy → `08_FACILITATOR`;
system decomposition → `09_IMPLEMENTATION_PLAN`.

