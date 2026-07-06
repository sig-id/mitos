# App Gap Report — security-audit, kanban-executor, conflict-resolver

These three applications are authored on the mitos stack (each is a real
content-addressed `app.yaml` + pipelines + capabilities under `beam/apps/`, and
each loads via `Mitos.App.Manifest`). Building them was the stress test: every
"the app needs X" that the substrate could not provide is a real gap, listed
below with the spec section that owns the missing mechanism and the smallest
slice that closes it.

The per-run paths work today (a single worker pipeline runs end-to-end against a
local repo through the harness adapters). The **cross-run orchestration** —
monitoring, fanout, and GitHub integration — is where the apps hit walls.

---

## G1. No scheduling / triggers (continuous monitoring) — blocks all three

**Spec owner:** `06 §6` (triggers: `cron · interval · continuous · event · webhook · external_signal`), `06 §8` (services).

- **security-audit** must "monitor continuously for any commits that landed on
  main" — a `continuous`/`cron` trigger over the repo. There is no trigger
  runtime: `Mitos.WorkPool` is an in-memory intake with no scheduler, no cron,
  no service units. The `main-commit-watcher` service the app `wants` does not
  exist.
- **conflict-resolver** must "monitor all PRs for conflicts" — an `event` trigger
  over PR webhooks. Same gap.
- **kanban-executor** wants a `kanban-sync` service (board ↔ issues).

**Smallest slice:** a `Mitos.Trigger` behaviour (`cron`, `event`, `webhook`) +
a service runtime that fires Tasks into the Work Pool on schedule. This is the
06 §6/§8 work that was deferred.

## G2. No fanout / concurrency — blocks kanban-executor

**Spec owner:** `06 §5` (disciplines, dependency graph, `invoke`/subtasks).

- The kanban planner emits N child Tasks (one per board task in a wave) expecting
  the Work Pool to dispatch up to `concurrency` (default 4) **concurrently with
  least conflict**. Today the Work Pool holds Tasks but has **no disciplines, no
  dependency resolver, no invoke/subtask, no worker pool** — a run is one
  pipeline, one process. There is no way to express "run these N Tasks at once,
  cap 4, respect deps."

**Smallest slice:** a worker dispatcher over the Work Pool + `Task.deps`/
`Task.invoke` resolution (06 §5). The app's `plan.json` already carries the wave
structure the dispatcher would consume.

## G3. No GitHub connector / sink — blocks issue+PR actions in all three

**Spec owner:** `10 §3`/`§5` (connector/sink behaviours — only `webhook`/`manual`
ingress and `notify`/`deliver`/`reply`/`web` sinks ship today).

- security-audit opens issues + PRs; kanban-executor opens PRs + syncs the board
  with issues; conflict-resolver pushes a fix. Today the apps **author the
  payloads** (`issues.json`, `prs.json`, `RESOLUTION.json`) but cannot *perform*
  the crossing — there is no `github` connector (for issue/board sync as ingress)
  and no `github` sink (for issue/PR creation, branch push). The skills correctly
  treat the actual push/open as a control-plane sink crossing (10 §5.1: a sink is
  not the sandbox broker), but the sink doesn't exist.
- The harnesses therefore must NOT call `gh` from inside the sandbox for these
  (that would smuggle a GitHub token into the sandbox and bypass the broker).
  The right shape is a `Mitos.Sink.GitHub` (creds in the vault, `delivery_sent`
  Effect per issue/PR) + a `Mitos.Connector.GitHub` for webhook/event ingress.

**Smallest slice:** `Sink.GitHub` (`gh`-or-API backed, vault-held token) +
`Connector.GitHub` (PR/issue webhooks → Tasks). Maps cleanly onto the existing
behaviours.

## G4. Memory is not injected into the run prompt — security-audit's profile

**Spec owner:** `07 §3` (retrieval injected as `/ctx` slices), `03 §2`.

- security-audit's whole point is a **durable security profile that updates as
  the project develops** and a **"last audited commit"** carried across runs.
  `Mitos.Memory` (built this round) stores both with supersession — but the run
  pipeline does not yet **inject** retrieved memory slices into the harness
  prompt. `LaunchSpec.context` is wired end-to-end, but nothing populates it from
  `Memory.retrieve/3` for the run's tenant/scope.

**Smallest slice:** at `prepare`, fetch the tenant's profile slices from Memory
into `LaunchSpec.context`. One wiring change in `Run`/`LaunchPolicy`; Memory +
the `/ctx` mount already exist.

## G5. Git worktree isolation per worker — kanban-executor

**Spec owner:** `05 §5.1` (workspace cloned IN), `01 §8.7` (`payload.workspace`).

- kanban-executor needs **one git worktree per concurrent worker** so N tasks
  don't clobber one checkout. The provider clones the workspace into the sandbox
  once, but there is no "allocate a worktree at `worktrees/<task>` and point this
  run at it" path, and no per-task branch hygiene. The kanban-planner assumes it
  can hand each child Task a distinct worktree path; nothing allocates it.

**Smallest slice:** a `Mitos.Git.Worktree` helper the planner/Run can call to
allocate + clean up worktrees, threaded through `workspace.source`.

## G6. OCI packaging / publish is not real — all three (publish path)

**Spec owner:** `12 §9`, `03 §9` (image build + OCI artifact). **Progress:** the
harness-rootfs build for the Nucleus provider is now wired end-to-end:
`beam/nucleus-rootfs/flake.nix` uses Nucleus's `lib.mkAgentToolchainRootfs`
(toolchain + providerPackages) to build an OCI bundle, and the Nucleus provider
runs it via `--bundle` (`config :mitos, :nucleus_bundle`) — argv-tested.

**Blocker on live verification (environment, not mitos):** Nucleus rootless gVisor
in this sandbox cannot run any workload — `gofer: fork/exec /proc/self/exe:
operation not permitted` (the gVisor gofer process can't fork under this rootless
userns). Verified directly: `nucleus create -- /bin/sh -c 'echo OK'` exits 128.
Needs either root, a privileged gVisor deployment, or a host where rootless gVisor
is permitted. The mitos-side path (rootfs flake + `--bundle` + `--credential-broker`
→ the TLS-MITM proxy) is complete and unit-tested; only the live Nucleus exec is
blocked here. Publish-to-registry (the OCI artifact push) remains unbuilt.

- Apps run from local directories (`ref: capabilities/<name>`). Publish resolves
  paths to digests **in identity only** (`app_id`); nothing builds a capability
  image or pushes an OCI artifact. A hosted deployment cannot pull/install these
  apps. This is the deferred image-build pipeline.

**Smallest slice:** a `Mitos.Image` build step (capability dir → OCI) + a
registry push behind the existing `Facilitator.publish`. Identity/digest math is
already correct; only the build+push is missing.

## G7. Persistence beyond the event log — all three (operated-at-scale)

**Spec owner:** `01 §4`, `09 §3`.

- Catalog, Vault, Memory, Facilitator (quotes/purchases/settlements), WorkPool,
  and I/O bindings are **in-memory**. A restart loses the published catalog, the
  tenant's security profile, the open board state, and the PR-watch bookkeeping.
  The 01 §4 invariant ("any answer recomputable from `fold(events)` with no
  process running") holds for *run state* only.

**Smallest slice:** durable backing for Catalog/Vault/Memory/WorkPool (a SQLite
or KV store behind the existing Agent surfaces — no API change).

---

## What the apps proved works

- **Harness adapters** are real and schema-pinned: claude-code, codex, opencode,
  pi all normalize their structured output (tested). An app declares any of them
  in `harnesses:` and it runs.
- **Nucleus gVisor provider** is wired (argv-verified) for strong local isolation;
  the launch policy admits it for T1. (True **remote T2** — separate kernel,
  E2B/Daytona — remains the gap for untrusted cross-tenant; Nucleus is local
  gVisor per `05 §3`.)
- **The agentic orchestrator** drives the **non-linear** kanban worker pipeline
  (plan ⇄ execute ⇄ gate ⇄ fix loops) behind the same seam as Linear, with a model
  client + deterministic fallback.
- **Memory** stores the security profile with contradiction-supersession and
  distills verified runs (G4 is just the injection wiring).
- **App packaging** is real: content-addressed `app_id`, entrypoints as the
  order form, validate-before-run, shipped I/O defaults.

## Priority for "launch these apps for real"

1. **G3 (GitHub connector/sink)** — unblocks the actual value (issues/PRs) of all
   three; pure additions behind existing behaviours.
2. **G1 (triggers/services)** — unblocks *continuous* operation; the apps are
   one-shot without it.
3. **G4 (memory injection)** — one wiring change; makes security-audit stateful.
4. **G2 (fanout)** — unblocks kanban concurrency; the largest single piece.
5. **G5, G6, G7** — worktree hygiene, real publish, persistence.
