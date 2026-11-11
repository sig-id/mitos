# Mitos specification

Mitos is an operating system for agents and deterministic programs. Services
preserve responsibilities, Tasks preserve work, and replaceable Processes execute
attempts under runtime-owned authority, resource limits, and durable records.

## Authority and conformance

Product and system specifications are **normative requirements**, including
requirements not yet implemented. MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY have
their RFC 2119 meanings. Unqualified behavioral statements in a contract are
requirements; examples, rationale, source reviews, and implementation evidence
are explanatory. A requirement is not a claim that the current build satisfies it.

- `product/` defines user-visible outcomes, interactions, and acceptance criteria.
- `system/` defines identities, protocols, invariants, execution, and recovery.
- `ADRs/` records decisions, alternatives, consequences, and supporting evidence.
  Superseded decisions and third-party observations impose no current requirements.

The [OS architecture](system/AGENT_OPERATING_SYSTEM.md) owns the system model.
The [durable runtime contract](system/DURABLE_RUNTIME.md) owns identity, authority
generations, delivery, recovery, and client/host separation. They take precedence
over earlier numbered specifications on those subjects. The numbered documents
retain their detailed subsystem contracts. Numeric references such as `01` resolve
through the catalog below. Historical BEAM/Elixir examples specify semantics only;
the implementation language is Go.

The [implementation ledger](system/IMPLEMENTATION_STATUS.md) and JSON files under
`system/evidence/` are descriptive evidence, not normative specifications. Dated
test results are historical observations. Local execution contracts define the
baseline migration MUST preserve. Target contracts add requirements; clients MUST
NOT advertise them as available before implementation and acceptance evidence
exist. New wire fields/endpoints require versioned schemas and compatibility tests.

## Product contracts

| Specification | Owns |
|---|---|
| [02 Product](product/02_PRODUCT.md) | Purpose, users, product boundaries and acceptance |
| [Workspace](product/WORKSPACE.md) | Projects, hosted agents, coordination, evidence journeys |
| [11 Frontend](product/11_FRONTEND.md) | Shared client semantics and commercial reader cuts |
| [18 Handheld A2UI](product/18_HANDHELD_A2UI.md) | Device interaction and rendering |
| [21 Handheld sound](product/21_HANDHELD_SOUND_FEEDBACK.md) | Audible feedback |

## System contracts

| Specification | Owns |
|---|---|
| [OS architecture](system/AGENT_OPERATING_SYSTEM.md) | Primitives, layers, responsibility and executor separation |
| [Durable runtime](system/DURABLE_RUNTIME.md) | Admission, fencing, receipts, waiting, federation, recovery |
| [01 Architecture](system/01_ARCHITECTURE.md) | Execution schemas, packaging, security boundaries |
| [03 Capability](system/03_CAPABILITY.md) | Packaged data, skills, tools, hooks and results |
| [04 Control plane](system/04_CONTROL_PLANE.md) | Pipeline policy and execution mechanisms |
| [05 Compute plane](system/05_COMPUTE_PLANE.md) | Provider isolation and launch policy |
| [06 Work pool](system/06_WORK_POOL.md) | Scheduling, dependencies, invocation and services |
| [07 Memory](system/07_MEMORY.md) | Authoritative statistics and proposed knowledge |
| [08 Facilitator](system/08_FACILITATOR.md) | Optional commerce, proof and settlement |
| [09 Implementation plan](system/09_IMPLEMENTATION_PLAN.md) | Migration and conformance gates |
| [10 I/O plane](system/10_IO_PLANE.md) | Connectors, sinks, ingress and delivery |
| [12 Application](system/12_APPLICATION.md) | Application packaging and entrypoints |
| [14 Observation](system/14_OBSERVATION.md) | Observation coverage and verified steering |
| [15 Decision plane](system/15_DECISION_PLANE.md) | Resolution policy and authority |
| [16 Autonomy](system/16_AUTONOMY.md) | Acceptance, checkpoints and supervision |
| [17 Device connector](system/17_HANDHELD_DECISION_CONNECTOR.md) | Versioned handheld protocol |
| [19 Wi-Fi setup](system/19_HANDHELD_WIFI_SETUP.md) | Device provisioning |

Local execution and API contracts:

- [Interactive workspace](system/INTERACTIVE_WORKSPACE.md),
  [project supervision](system/PROJECT_SUPERVISION.md),
  [agent adapters](system/AGENT_CLI_ABSTRACTION.md),
  [continuation](system/SESSION_CONTINUATION.md), and
  [terminal streaming](system/TERMINAL_STREAMING.md).
- [Compute](system/COMPUTE.md), [workspaces and acceptance](system/WORKSPACES.md),
  [observation](system/OBSERVATION.md), [recovery](system/RECOVERY.md),
  [decisions](system/DECISIONS.md), and [device API](system/DEVICE_API.md).
- [Fleet](system/FLEET.md), [work pools](system/WORK_POOLS.md),
  [artifact dependencies](system/DATA_DEPENDENCIES.md),
  [shared workspace coordination](system/SHARED_WORKSPACE_COORDINATION.md), and
  [autonomy metrics](system/AUTONOMY_METRICS.md).
- [Services](system/SERVICES.md) and its linked trigger, lifecycle, health,
  dependency, priority, replacement, retry, and circuit-breaker contracts.

## Decisions

| ADR | Status |
|---|---|
| [0001 Terminal renderer](ADRs/0001_TERMINAL_RENDERER.md) | Accepted: retain xterm.js; dated evaluation evidence |
| [0002 Batch console](ADRs/0002_BATCH_CONSOLE.md) | Superseded as primary navigation; retained rationale |
| [0003 Competitive context](ADRs/0003_COMPETITIVE_CONTEXT.md) | Historical source review; informational |
| [0004 Durable agent OS](ADRs/0004_DURABLE_AGENT_OS.md) | Accepted: common runtime and replaceable executors |

Specification changes MUST update affected contracts and acceptance criteria.
Architecture changes MUST record the decision and consequences in an ADR. New
implementations MUST report satisfied requirements and remaining gaps; moving a
document or renaming a concept does not establish conformance.
