# Mitos specification

Start with [the product](product/01_PRODUCT.md): why Mitos exists, who it serves,
its market, and the experience it promises. Then read [architecture](system/01_ARCHITECTURE.md)
for the system model and [delivery](system/06_DELIVERY_AND_CONFORMANCE.md) for the
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

Each directory is numbered independently. Product has one document; system is
grouped by architectural responsibility. There is no fixed document quota. Add
material to its owner and consolidate overlap before introducing another document.
UI details, historical test logs, sample evidence files, and obsolete designs do
not belong in this catalog. Tool/deployment instructions may remain with their code.

## Product

- [01 Product](product/01_PRODUCT.md) – purpose, users, market, offering, server/client experience, and product commitments.

## System

| Specification | Owns |
|---|---|
| [01 Architecture and deployment](system/01_ARCHITECTURE.md) | Model, layers, servers, clients, multi-server ownership, and failure boundaries |
| [02 Execution, authority, and recovery](system/02_RUNTIME.md) | Lifecycle, grants, resources, admission, persistence, scheduling, and recovery |
| [03 Supervision, services, and decisions](system/03_ORCHESTRATION.md) | Coordination, messages, activation, events, service lifecycle, and human resolution |
| [04 Workspaces, verification, and knowledge](system/04_STATE_AND_VERIFICATION.md) | Artifacts, acceptance, checkpoints, observation, provenance, and metrics |
| [05 Clients, applications, and integrations](system/05_INTERFACES_AND_INTEGRATIONS.md) | APIs, security, streaming, adapters, packaging, connectors, and compatibility |
| [06 Delivery and conformance](system/06_DELIVERY_AND_CONFORMANCE.md) | Current baseline, implementation order, and acceptance gates |

## Decisions

- [0001 Durable agent operating system](ADRs/0001_DURABLE_AGENT_OS.md) – accepted; common runtime, replaceable executors, and lessons from Orca.
- [0002 Specification structure and scope](ADRs/0002_SPECIFICATION_STRUCTURE.md) – accepted; one product spec and system contracts grouped by responsibility.
