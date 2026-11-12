# ADR 0005: Shared local and application-server runtime

Status: accepted, 2026-11-12; implementation pending.
Contracts: [product](../product/01_PRODUCT.md#application-backend-use-case-acegoat),
[architecture](../system/01_ARCHITECTURE.md#application-server-deployment),
[interfaces](../system/05_INTERFACES_AND_INTEGRATIONS.md#application-and-worker-server-contract),
[delivery](../system/06_DELIVERY_AND_CONFORMANCE.md#application-server-delivery-gates).

## Context

Mitos began as a local agent operating system. Acegoat, a separately owned product
in architectural design, needs persistent personal agents, bounded tasks, replaceable
workers, durable waiting and safe recovery. Building those mechanisms independently
would duplicate Mitos's runtime responsibilities. No Acegoat implementation or Rust
store/scheduler referenced in earlier analysis is an existing Mitos foundation.

## Decision

Extend Mitos with a headless application-server profile over the same runtime core.
Keep Acegoat in its own repository with its own backend, UI, domain models and release
cycle. It consumes versioned Mitos APIs rather than internal packages, runtime tables,
a fork, or a copied scheduler. No monorepo or embedded Go module is required.

Preserve local startup and journal persistence. Plan a PostgreSQL runtime adapter for
hosted deployments, structured tasks without filesystem prerequisites, tenant-scoped
application access, and a separately versioned remote worker protocol. This extends
ADR 0001's local-first deployment and storage decision; it does not replace the local
journal or authorize replicated writers and automatic failover.

Assign each lifecycle one authority. Acegoat owns agents as domain identities,
conversations and business outcomes; Mitos owns execution Tasks, Runs and supervisory
bindings. Integrate the separate transaction boundaries through durable outboxes,
stable operation identities, deduplicated publication and reconciliation. Sharing a
database host does not create an atomic transaction between the services.

Keep MQTT as a notification/event transport under ADR 0002. Runtime claims and
application receipts establish ownership and acceptance. Preserve resource custody:
lease expiry cannot prove an old worker has stopped or an external effect failed.

## Alternatives considered

- Build Acegoat's own runtime: permits independent design but duplicates scheduling,
  replacement, receipts and recovery, with diverging guarantees.
- Put both products in one repository or require in-process embedding: simplifies
  initial changes but conflicts with independent product ownership and deployment.
- Require hosted infrastructure for every Mitos installation: simplifies adapter
  selection but adds unnecessary operational cost to the local product.
- Make both services write shared task tables: hides ownership and bypasses protocol
  validation, migration compatibility and receipt boundaries.

## Consequences

The shared runtime gains deployment adapters and explicit application boundaries.
Hosted isolation, persistence and remote execution add real implementation work;
existing local behavior is not evidence that those guarantees are complete. S1–S4
require an end-to-end message/reply path with crash recovery before broader negotiation
use. Domain-specific prompts, applicant visibility policy and customer UI stay in
Acegoat. A future SDK may simplify integration without becoming a second runtime.

The local product remains the initial delivery milestone. Server work can proceed
incrementally against the same lifecycle contracts, with independent conformance
claims and no requirement to implement federation first.
