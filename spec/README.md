# Mitos specification

Start with [the product](product/01_PRODUCT.md): why Mitos exists, who it serves,
its market, and the experience it promises. Then read [architecture](system/01_ARCHITECTURE.md)
for the system model and [delivery](system/15_DELIVERY_AND_CONFORMANCE.md) for the
current baseline and remaining work.

## Authority

Product and system specifications are normative, including requirements not yet
implemented. MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY express requirement levels.
Unqualified contract behavior is required unless explicitly marked as an example,
rationale, market assessment, or current-baseline observation. A requirement is
not a claim that the current build satisfies it.

Product defines outcomes and scope. System defines the contracts that deliver them.
ADRs explain decisions and alternatives; they do not create a competing contract.
Implementation gaps MUST remain explicit. Conflicts MUST be resolved in the owning
specification rather than hidden by a precedence rule between overlapping documents.

Each directory is numbered independently. Product has one document; system has
15. Add material to its owning document before proposing another specification.
UI details, historical test logs, sample evidence files, and obsolete designs do
not belong in this catalog. Tool/deployment instructions may remain with their code.

## Product

- [01 Product](product/01_PRODUCT.md) – purpose, users, market, offering, server/client experience, and product commitments.

## System

| Specification | Owns |
|---|---|
| [01 Architecture](system/01_ARCHITECTURE.md) | Primitives, compositions, layers, and invariants |
| [02 Server and clients](system/02_SERVER_AND_CLIENTS.md) | Deployment, connection, execution hosts, and failure boundaries |
| [03 Work and execution](system/03_WORK_AND_EXECUTION.md) | Identity, admission, lifecycle, cancellation, and replacement |
| [04 Durability and recovery](system/04_DURABILITY_AND_RECOVERY.md) | Journal, transactions, receipts, replay, and uncertain effects |
| [05 Authority and resources](system/05_AUTHORITY_AND_RESOURCES.md) | Grants, claims, budgets, and provider enforcement |
| [06 Agents and supervision](system/06_AGENTS_AND_SUPERVISION.md) | Coordination, delegation, adapters, and continuation |
| [07 Scheduling and dependencies](system/07_SCHEDULING_AND_DEPENDENCIES.md) | Pools, readiness, artifact inputs, and durable waiting |
| [08 Services and events](system/08_SERVICES_AND_EVENTS.md) | Activation, schedules, ingress, retries, health, and lifecycle |
| [09 Workspaces and artifacts](system/09_WORKSPACES_AND_ARTIFACTS.md) | Layout, snapshots, harvest, integrity, and retention |
| [10 Verification and checkpoints](system/10_VERIFICATION_AND_CHECKPOINTS.md) | Acceptance, criteria, restoration, and assurance |
| [11 Communication and decisions](system/11_COMMUNICATION_AND_DECISIONS.md) | Mailboxes, human resolution, silence, and notifications |
| [12 Observation and knowledge](system/12_OBSERVATION_AND_KNOWLEDGE.md) | Coverage, stalls, provenance, context, and metrics |
| [13 Applications and integrations](system/13_APPLICATIONS_AND_INTEGRATIONS.md) | Packaging, execution contracts, connectors, and sinks |
| [14 API and compatibility](system/14_API_AND_COMPATIBILITY.md) | Shared operations, streaming, device scope, and migration |
| [15 Delivery and conformance](system/15_DELIVERY_AND_CONFORMANCE.md) | Current baseline, implementation order, and acceptance gates |

## Decisions

- [0001 Durable agent operating system](ADRs/0001_DURABLE_AGENT_OS.md) – accepted; common runtime, replaceable executors, and lessons from Orca.
- [0002 Specification structure and scope](ADRs/0002_SPECIFICATION_STRUCTURE.md) – accepted; one product spec and 15 consolidated system contracts.
