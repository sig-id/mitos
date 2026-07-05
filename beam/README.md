# mitos — BEAM control plane

The clean-room implementation of `spec/` on Elixir/BEAM: the substrate that
runs packaged capabilities through agent-driven pipelines, where an agent
decides policy and deterministic code records every decision and enforces
every limit (`spec/01_ARCHITECTURE`).

Scope: the **launch-path cut of `spec/09_IMPLEMENTATION_PLAN`** — one thin vertical
slice through the component map (§1) that reaches the launch gate (§2: *one
stranger-paid, machine-fulfilled transaction, with proof*), plus the seams the
later breadth fills.

## Run it

```sh
mix deps.get && mix test          # 38 tests: crash test, boundary, gate e2e
mix escript.build

# local run of a pipeline against a capability dir
./mitos run-pipeline pipeline.md --cap greeter=./caps/greeter --workspace ./input

# an application entrypoint (12 §4)
./mitos run ./apps/greeting-studio --entrypoint greet -i name=Ben

# reads of the log
./mitos log    <run_id>           # full-fidelity event log (jsonl)
./mitos status <run_id>           # fold(events)
./mitos proof  <run_id>           # buyer-facing redacted proof (08 §5)

# I/O plane (10): connectors in / sinks out
./mitos deliver <run_id>          # idempotent egress re-drive for a finished run
./mitos import eve <agent_dir>     # Eve agent/ → app scaffold (the import funnel, 12 §10)

# hosted surface
./mitos serve                     # API on :4646, broker ingress on 127.0.0.1:4647
```

The paid endpoint (08 §7/§8): `POST /capabilities/{id}/runs` — no `X-PAYMENT`
header returns `402` with an x402 quote; a verified payment admits the run.
Delivery: `GET /runs/{run_id}/deliverable` + `GET /runs/{run_id}/proof`.

The I/O plane (10): `POST /ingest/{org}/{connector}` turns an inbound webhook
into a Task → run (`signal_received` + `task_enqueued` land on the log);
`POST /runs/{run_id}/deliver` is the idempotent egress re-drive (sinks fire,
`delivery_*` Effects appended).

## The web UI (11)

`./mitos serve` also serves the web surfaces at `http://localhost:4646/web` —
server-rendered from the BEAM, no client framework, no build step. Every page
is `render(fold(events), cut)` (11 §1).

**Mock identity:** `/web/login` offers three roles (operator / seller / buyer)
as a cookie — a stand-in for an IdP. The role selects the navigation and the
redaction cut, never a different data source (08 §2): operator gets the dev
cut, seller gets attribution, buyer gets the commercial cut. Buyer
`watch`/`proof` URLs work without sign-in — buy is a URL, no account (11 §9) —
and are redacted by construction. Surfaces:

| Page | Surface (11) | Cut |
|---|---|---|
| `/web/login` | role picker (mock IdP seat) | — |
| `/web` | run history (v1 operate surface, §8) | dev |
| `/web/apps` → entrypoint **order form** → run | Run & Watch entry (§4, 12 §4) | dev |
| `/web/runs/{run_id}` | **watch**, outer zoom: Decision/Effect timeline, auto-refresh while live | dev (rationale + inner links) |
| `/web/runs/{run_id}/trace/{stage}` | **inner zoom**: the harness's own steps (persisted per stage) | dev only |
| `/web/runs/{run_id}/report` | the Deliverable (§5) | dev |
| `/web/runs/{run_id}/proof` | the **same render** | commercial (= 08 §5 proof + receipt) |
| `/web/runs/{run_id}/watch` | buyer's read-only watch link | commercial |
| `/web/capabilities` → order page | buy is a URL (§9) | — |
| `/web/attribution` | seller's flat revenue list (§7 MVP) | seller |

The one-renderer/two-cuts rule (11 §5) is enforced by construction: the
commercial pages are built from Effect events only — no `rationale`, no inner
trace, no harness identity ever reaches them.

## Implementation ↔ spec map

| Module | Spec | Role |
|---|---|---|
| `Mitos.Event.{Decision,Effect}` | 01 §8.1/8.2, 04 §5/§6 | the two canonical log record types |
| `Mitos.Log` | 01 §4, 04 §11 | append-only, fsync-before-execute, single writer per run |
| `Mitos.Run.State` | 01 §4, 04 §7 | `state = fold(events)`; pending-Decision recovery tail |
| `Mitos.Run` | 04 §3.4/§7, 03 §6 | run process: control loop, step runtime (hooks→loop→harvest), budget refusal, resume/re-drive |
| `Mitos.Run.Config` | 09 §4 | durable per-run config (tenant-stamped run_id, no secrets) |
| `Mitos.Orchestrator.Policy` (+ `.Linear`, `.Agentic`) | 04 §3 | the orchestrator seam; deterministic Linear + LLM Agentic (model seam + Linear fallback) |
| `Mitos.Orchestrator.Model` (+ `.Stub`, `.HTTP`) | 04 §3.4 | the model client seam (OpenAI-compatible HTTP; Stub ⇒ Linear fallback) |
| `Mitos.LoopManager` | 04 §4 | intra-step FSM; MVP one iteration, signals seam in place |
| `Mitos.Pipeline` | 04 §8 | loose markdown intent parser (never rejects for shape) |
| `Mitos.Capability.Manifest` | 01 §8.3, 03 §8/§9 | content-addressed capability identity |
| `Mitos.Capability.Skill` | 03 §2.1 | `resolved = base ⊕ per_model_adapt` (identity law holds) |
| `Mitos.Capability.Result` | 03 §7 | soft result + evidence harvest, synthetic `unknown` |
| `Mitos.Hooks` | 03 §6 | deterministic pre/post gates, Effect-logged |
| `Mitos.SandboxProvider` | 01 §8.4, 05 §2 | the one provider behaviour |
| `Mitos.LaunchSpec` / `Mitos.LaunchPolicy` | 05 §2/§4 | policy compiler; T2 fails closed, never downgrades |
| `Mitos.Provider.Local` | 05 §3 (seam) | local process-isolation provider: hermetic inject, git-baseline harvest |
| `Mitos.Provider.Nucleus` | 05 §3 | gVisor-class local isolation via `nucleus run` (T1; remote T2 still a gap) |
| `Mitos.Harness` (+ `.Fake`, `.ClaudeCode`, `.Codex`, `.Opencode`, `.Pi`) | 01 §8.6, 05 §6 | adapter contract + impls; per-vendor structured-output normalization + `argv/2` |
| `Mitos.Harness.PortAdapter` | 01 §8.6 | shared subprocess + line-stream machinery for structured-output adapters |
| `Mitos.Memory` | 07 | two-track knowledge + contradiction-supersession + verify→distill |
| `Mitos.Broker` (+ `.Listener`, `.Destinations`) | 01 §8.5, 05 §5.3 | egress credential broker: default deny, attach-at-edge, every crossing an Effect |
| `Mitos.Vault` | 08 §3.2 | credentials exist only here, keyed `(scope, dest)` |
| `Mitos.Hermetic` | 05 §6 | full-env-clear subprocess environments; host git/gpg neutralized |
| `Mitos.Meter` | 08 §4 | charge = deterministic fold over the log (take rate, floor, refunds) |
| `Mitos.Proof` | 08 §5 | buyer proof from Effects only (rationale/harness/skill redacted), log-hash anchor |
| `Mitos.Settlement` (+ `.Mock`) | 08 §7 | x402 rail seam: verify / escrow / split / refund |
| `Mitos.Facilitator` | 08 §7–§9 | publish → quote → admit → run → settle |
| `Mitos.Task` / `Mitos.WorkPool` | 01 §8.7, 06 | durable Task intake (thin; pools/deps/disciplines beyond launch) |
| `Mitos.App.Manifest` | 01 §8.8, 12 | application bundle, content-addressed `app_id`, entrypoints |
| `Mitos.App` | 12 §4/§5 | app lifecycle: add (activate shipped I/O defaults), validate-before-run entrypoint → run |
| `Mitos.Import.Eve` | 12 §10, 03 §10.1 | the import funnel: an Eve `agent/` → a draft T2 app scaffold |
| `Mitos.Connector` (+ `.Webhook`, `.Manual`) | 10 §3 | ingress behaviour + impls: external signal → idempotent intake → Task |
| `Mitos.Sink` (+ `.Notify`, `.Deliver`, `.Reply`, `.Web`) | 10 §5 | egress behaviour + impls: terminal Effect → redacted render → delivery |
| `Mitos.IO` (+ `.Binding`) | 10 §2/§4/§7 | bindings registry, ingress intake (dedup + select), idempotent delivery driver |
| `Mitos.Web.Router` | 01 §10.11, 08 §8, 10 §6 | HTTP surface = the same backend ops the CLI calls; the web app is one connector + sink |
| `Mitos.CLI` | 11 §7 | escript: run / log / status / proof / publish / serve |

## Applications on the stack (`apps/`)

Three real, content-addressed mitos apps (each loads via `Mitos.App.Manifest`):

- **`security-audit`** — profile a repo's security scope, audit a commit range,
  classify findings P0–P3, author issues + fix PRs. The durable profile lives in
  `Mitos.Memory`.
- **`kanban-executor`** — plan a dependency/concurrency-aware fanout over a
  kanban board and run each task through a **non-linear** worker pipeline
  (plan ⇄ execute ⇄ gate ⇄ fix) via the **agentic orchestrator**.
- **`conflict-resolver`** — resolve PR merge conflicts with the smallest safe
  change and push.

The per-run pipelines run on the stack today. The cross-run orchestration each
app needs (continuous triggers, fanout, GitHub issue/PR integration) is the gap
analysis in **[`apps/GAPS.md`](apps/GAPS.md)**.

## Judgment calls carried from the spec

- **Sellers are curated, runs execute at T1 locally** (09 §2, flagged for
  sign-off). `config :mitos, :marketplace_tier` is the knob; setting it to
  `"T2"` today *fails closed* (no remote provider is registered) and refunds
  the buyer — distrust never relaxes isolation (05 §4). E2B/Daytona providers
  are a later `SandboxProvider` impl.
- **`Provider.Local` is process-level isolation** and is honest about it
  (`capabilities().isolation = :process`), so the policy compiler admits it
  for T0/T1 only. The Nucleus/gVisor provider plugs into the same behaviour.
- **The orchestrator is the deterministic `Linear` policy** behind the
  real seam (`Mitos.Orchestrator.Policy`): one Decision per turn, budget on
  every Decision, append-before-execute. The LLM orchestrator swaps in without
  touching the substrate.
- **Broker approvals fail closed** (the `approval_requested` Effect is
  emitted; the interactive approve→forward leg is beyond launch).
- **The I/O plane (10) is in scope** for what a paid job needs: `webhook` +
  `manual`/`web` ingress and `notify`/`deliver`/`reply`/`web` sinks, all as
  Effects on the run log (no parallel ledger). Chat/email/calendar connector
  breadth is commodity and demand-gated (10 §8).
- **Deferred wholesale** (beyond launch, per 09 §1): remote providers, work-pool
  disciplines/deps/invoke/services, memory + self-improvement, image
  build pipeline, multi-step branching, snapshot resume, `smart` selection.
- **Identity is external.** Buyer/seller/operator identity are asserted by
  **sigid** (the external IdP, 01 §0, 08 §3.1); mitos consumes assertions and
  never runs its own IdP. The web role cookie is a mock seat for that seam.
