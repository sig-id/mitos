# ADR 0005: Shared local and application-server runtime

Status: accepted, 2026-11-12; storage amended 2026-11-17 (issue #77); implementation pending.
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

Preserve service-free local startup and canonical journal semantics. SQLite is the
transactional runtime store for local and application-server deployment; a PostgreSQL
server edition is deferred and is outside the S2–S4 implementation plan in issue #77.
Keep structured tasks without filesystem prerequisites, tenant-scoped application
access, and a separately versioned remote worker protocol. Under the amended
ADR 0001, SQLite replaces JSONL persistence through a verified one-time import with
the original retained as backup. There is one local adapter, not a dual-adapter mode.
This does not authorize replicated writers or automatic failover.

Store the existing Event records and hash chain in an `events` table keyed by `seq`;
retain apply and replay. Write rebuildable projections in the same transaction as
the journal, rather than maintaining a second set of relational lifecycle semantics.
Use `modernc.org/sqlite`, WAL and `synchronous=FULL`, with the existing process lock
on a lock file and the sequence primary key as an additional guard. Forward-only
embedded migrations MUST reject unknown or newer schemas. A commit error latches
the writer unavailable until recovery re-reads the durable tail. Online backup uses
the SQLite backup API; restore verifies the hash chain and advances an authority
epoch to fence old leases and tokens. Expired request keys retain rejection
tombstones. Exact agent-library revisions must be included for complete backups.

Remote placement is limited to headless structured work. Workspace, PTY and graph
execution remain local. Context reads, result commits and external actions pass
through Mitos-controlled boundaries so that revocation can be enforced. Local and
remote structured execution share the same worker handlers and scheduler; HTTP is
the remote transport, not another execution path.

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
- Keep JSONL and SQLite as two local adapters: duplicates persistence semantics
  and conformance work without a user benefit. The one-time import is the only
  migration path.
- Implement PostgreSQL first: adds an external service to the primary single-user
  deployment; defer it to a separately demonstrated server edition.
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
claims and no requirement to implement federation first. `hosted.postgresql` stays
false until its own adapter and PostgreSQL-specific S2 acceptance are demonstrated;
SQLite does not close that part of issue #54.
