# Current implementation and technology decision

**2026-09-08: replace the BEAM implementation with Go + HTMX and redesign the
product from these specs.** This decision supersedes implementation-language
requirements and old frontend navigation in the remaining specs. Product
contracts and invariants remain targets, not claims of implemented features.

The old `beam/` tree, build artifacts, Mix configuration, tests, and runtime
applications are removed. Repository-root Go tooling is the only implementation.
Historical Elixir callback examples in detailed specs describe target semantics;
future Go interfaces must preserve those semantics rather than their syntax.
External engine, Nucleus, and sigid boundaries remain external.

## Implemented

- Go server/CLI and embedded HTMX UI; no frontend build or CDN dependency.
- New Studio navigation: Overview, Work, Decisions, Applications.
- Loopback-only operation with cross-origin mutation protection.
- Durable task/run identity, JSONL Decision/Effect records, hash chaining,
  fsync-before-execute, locked writer, replay, and partial-tail recovery.
- Brief validation, bounded workspace copying, concurrent local execution,
  fixed installed commands, cancellation, timeouts, retries.
- Built-in workspace inventory with actual hashes and a portable report.
- Small JSON local-app dialect: one entrypoint, command plus acceptance.
- Separately executed acceptance; saved output and checksum-checked downloads.
  **The local same-user provider does not enforce oracle isolation.**
- Interrupted-run recovery and explicit, idempotent external decision cards.
- Search, run details, execution timeline, standalone operator reports.
- Labelled, opt-in example history using a separate data directory.

## Not yet implemented

This is a functioning local vertical slice. It satisfies **neither** the Studio
overnight gate nor the Market launch gate in `09`.

| Target | Missing mechanisms |
|---|---|
| Agentic control (`04`) | Model policy, composition, hooks, harness adapters, loop manager |
| Observation (`14`) | Session tails, quiescence, terminal snapshots, verified steer |
| Autonomy (`16`) | Isolated oracle, tamper detection, checkpoints, rewind, supervision, evaluation and metrics |
| Decisions (`15`) | Policy classification, safe defaults/TTL, sandbox rehydration, notifications/remote resolution |
| Compute (`05`) | T2 isolation, mounts, egress broker/vault, git-aware snapshots and patch harvest |
| Packaging (`03`, `12`) | Original YAML manifests, typed schemas, content-addressed identity, OCI publish/import |
| Work pools / I/O (`06`, `10`) | Dependencies, disciplines, invoke, services, connectors, sinks |
| Memory (`07`) | Retrieval, verification/distillation, supersession |
| Market (`08`) | Identity/tenancy, keys, paid admission, metering, settlement, commercial proof, attribution |

`docs/PRODUCT_DESIGN.md` records the product interpretation. `README.md` records
the actual command, manifest, data, and execution contracts. Sample outcomes
are illustrative; a local acceptance exit code is not the inaccessible oracle
required by `16 §3.2`.
