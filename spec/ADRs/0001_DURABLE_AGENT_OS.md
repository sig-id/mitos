# ADR 0001: Durable agent operating system

Status: accepted, 2026-11-11.
Contracts: [OS architecture](../system/01_ARCHITECTURE.md),
[durable runtime](../system/02_RUNTIME.md), [product](../product/01_PRODUCT.md).

## Context

Mitos has durable batch Tasks, acceptance, services, and checkpoints, alongside a
separate interactive Session store and trusted-local PTY execution. The operating
system direction requires consistent identity and guarantees across these paths.
The earlier capability/commerce architecture made an LLM pipeline and cloud-first
hosting foundational. Those assumptions exclude useful deterministic and local
workflows and do not resolve the interactive/batch split.

The Orca review inspected source and documentation at commit
[`1a4b0c76b1859d2ec4b905299cf7b7596fed11c6`](https://github.com/stablyai/orca/commit/1a4b0c76b1859d2ec4b905299cf7b7596fed11c6).
It was a static review, not a runtime benchmark or exhaustive security assessment.
Orca already implements meaningful parts of durable coordination; Mitos cannot
claim that persistent Tasks or coordinator replacement alone are novel.

## Evidence and adopted lessons

| Inspected Orca mechanism | Evidence | Mitos decision |
|---|---|---|
| Desktop/mobile/browser clients connect to an execution-owning runtime; SSH and paired servers are different modes | [Deployment modes](https://github.com/stablyai/orca/blob/1a4b0c76b1859d2ec4b905299cf7b7596fed11c6/docs/site/content/docs/ways-to-run.mdx), [mobile RPC](https://github.com/stablyai/orca/blob/1a4b0c76b1859d2ec4b905299cf7b7596fed11c6/mobile/README.md) | Separate interfaces, authority, and execution placement. |
| Background daemon owns PTYs across app exit; reboot still ends processes | [Session restore](https://github.com/stablyai/orca/blob/1a4b0c76b1859d2ec4b905299cf7b7596fed11c6/docs/site/content/docs/model/session-restore.mdx) | Advertise process survival separately from durable work and host recovery. |
| Run namespace, Tasks, Dispatch attempts, messages and gates | [Orchestration](https://github.com/stablyai/orca/blob/1a4b0c76b1859d2ec4b905299cf7b7596fed11c6/docs/site/content/docs/cli/orchestration.mdx) | Preserve assignment/attempt separation and a stable supervisory inbox. Keep Mitos Run meaning. |
| SQLite WAL and transactions persist orchestration state | [Database](https://github.com/stablyai/orca/blob/1a4b0c76b1859d2ec4b905299cf7b7596fed11c6/src/main/runtime/orchestration/db/orchestration-db.ts) | Adopt atomic admission/settlement; retain Mitos's canonical journal pending a separate storage decision. |
| Coordinator rebinding advances consumer generation and fences deliveries | [Binding](https://github.com/stablyai/orca/blob/1a4b0c76b1859d2ec4b905299cf7b7596fed11c6/src/main/runtime/orchestration/db/runs/run-binding.ts), [delivery](https://github.com/stablyai/orca/blob/1a4b0c76b1859d2ec4b905299cf7b7596fed11c6/src/main/runtime/orchestration/db/runs/run-delivery.ts) | Make coordinator ownership and consumer fencing runtime contracts. |
| Caller/request identity and payload hash bind pending/completed mutation receipts | [Receipts](https://github.com/stablyai/orca/blob/1a4b0c76b1859d2ec4b905299cf7b7596fed11c6/src/main/runtime/orchestration/db/mutation-receipts/mutation-receipt-store.ts) | Make retriable mutations durable; preserve ambiguous outcomes. |
| Worker authority binds attempt, process identity, launch-token commitment and terminal custody | [Worker authority](https://github.com/stablyai/orca/blob/1a4b0c76b1859d2ec4b905299cf7b7596fed11c6/src/main/runtime/orchestration/db/worker-dispatch/worker-dispatch-authority.ts) | Fence stale workers and retain resource custody through recovery. |
| Inspected worker-report settlement accepts an authorized reported outcome | [Settlement](https://github.com/stablyai/orca/blob/1a4b0c76b1859d2ec4b905299cf7b7596fed11c6/src/main/runtime/orchestration/db/dispatch-context/worker-report-settlement.ts) | Keep reported success separate from independently accepted completion. |
| Relay cells forward outbound WebSocket connections; separate gateway delivers push | [Relay](https://github.com/stablyai/orca/blob/1a4b0c76b1859d2ec4b905299cf7b7596fed11c6/cloud/README.md) | Treat relay/push as optional transport, never the execution authority. |

These observations establish the inspected paths, not universal absence of other
Orca mechanisms. Its orchestration and environment recipes are documented as
experimental, and remote/mobile surfaces as beta at this revision.

## Decision

Adopt six primitives: Task, Process, Resource, Grant, Channel, and Record.
Services and supervisory responsibilities compose them. Role, executor, and
activation are independent. A durable agent is a recoverable logical
responsibility executed by replaceable processes.

Use one common runtime contract beneath interactive and automated work. Separate
clients, home authority, and execution hosts; support local deployment first and
explicit remote placement subsequently. Keep Go, existing public identifiers,
and current journal semantics. Add request receipts, authority generations,
control mailboxes, and durable wait registration before general federation.

Each project has one home server, each server may own multiple projects, and clients
can aggregate several independently paired servers. This does not require a global
primary or server-to-server coordination. Remote placement and transferring a
project's home remain separate capabilities with explicit authority boundaries.

Extend continuity to grants, budgets, resource ownership, verified results,
knowledge provenance, and unresolved external effects. Generations fence control
requests; actual resource safety still requires provider enforcement or confirmed
stop. Recovery classifies pending operations rather than blindly repeating them.

Product/system specifications are normative. Current implementation and outstanding
gates are identified in delivery and conformance. Commerce is outside the initial
product scope and requires a separate decision before implementation.

## Alternatives considered

- Retain separate batch and interactive control planes: smaller immediate changes,
  but duplicate admission, lifecycle, and recovery rules would continue to diverge.
- Preserve only conversations and terminal processes: useful continuity across UI
  exit, but insufficient for authority, verified progress, and host failure.
- Copy Orca's architecture/storage directly: proven ideas merit adoption, but its
  Run meaning and persistence model would require unnecessary compatibility changes.
- Start with replicated multi-host failover: adds consensus and split-brain risks
  before a single authority has a complete recovery contract.

## Consequences

Migration needs explicit Session/Process/Attempt bindings and one writer during
cutover. Existing local behavior and runtime records remain compatible, but strict claims
require actual provider enforcement. Deployment separation introduces a versioned
process-host protocol and inspection receipts. Durable operations require bounded
receipt retention, cancellation, mailbox recovery, and fault-injection tests.

No native mobile application, relay service, SQLite migration, or automatic host
failover is committed by this decision. Clients can be added against the shared
contract as needed. The first acceptance milestone is local task continuity with
verified replacement, followed by paired clients and remote execution.
