# ADR 0004: Adaptive work graphs with deterministic admission

Status: accepted design, 2026-11-11; implementation pending G1–G5.
Extends [ADR 0001](0001_DURABLE_AGENT_OS.md) and
[ADR 0003](0003_DECISION_MODELS_AND_RUNNER.md).

## Context and evidence

The original UI separated single-agent work, supervised work, and automation. These
mix planning policy, executor type, and activation. They cannot express a scheduled
supervisor, deterministic parallel agent work, or a fixed verification obligation
inside an otherwise adaptive investigation without special cases.

Chief Wiggum was reviewed from the sibling checkout `../chief-wiggum` at commit
`f11e476037819b9cd15ad00cc16bfd0956481aed`. This was static source/document review,
not an execution, scale, or isolation benchmark. The inspected mechanisms were:

| Source path in that revision | Finding | Adopted lesson |
|---|---|---|
| `lib/scheduler/scheduler.sh`, `lib/tasks/task-parser.sh` | Ready-set calculation, priority/aging, dependency checks, capacity and file-conflict filters | Separate scheduling from worker reasoning and make readiness explainable. |
| `spec/PIPELINE_SCHEMA.md`, `lib/pipeline/pipeline-runner.sh` | Ordered steps, result-based jumps, inline remediation, visit counters | Give transitions explicit semantics, but generate task topology from evidence rather than requiring all branches in advance. |
| `spec/RUNTIME_SCHEMA.md`, `lib/runtime/runtime.sh` | Backend invocation/session/retry contracts | Keep executor adapters independent of workflow policy. |
| `spec/AGENT_SPEC.md`, `lib/runtime/runtime-loop.sh` | Declarative agents, prompt/context assembly, result tags and supervisor feedback | Reuse templates and context patterns; do not require framework-specific reply tags from Mitos workers. |

Chief Wiggum is configurable, not simply a fixed linear pipeline. Its engineering
recipes and Git/PR lifecycle are useful applications, but should not become the
universal Mitos kernel model. New planning and decision capabilities make it practical
to construct work and recovery paths as evidence arrives. This does not remove the
need to define operational failure, authority, custody, and completion precisely.

Provider research in ADR 0003 establishes typed decision inference. OpenAI
[Decisions](https://developers.openai.com/api/docs/guides/decisions) evaluates
predicates, supplied choices, and scores; arbitrary object generation is a separate
capability. Jev's [Choice](https://docs.typesafe.ai/primitives/choice) also selects
supplied alternatives. Consequently, candidate generation and decision evaluation
must remain distinct, even when one controller coordinates both. Dynamic candidates
do not require a static graph of every possible future task.

## Decision

Adopt `work-graph/v1` as the target composition contract over existing Tasks and
Records. Retain Run as Attempt and retain existing adapter/runtime versions.
Adaptive composition is the default product path; defined and hybrid policies remain
supported through the same admission interface. Activation and executor choice are
independent of planning policy.

A controller can generate concrete task specifications, dependencies, domain/tool
operations, agent specializations, and recovery work as execution progresses. Typed
decisions can select or assess these candidates. The kernel validates and commits
bounded amendments atomically; the scheduler admits eligible tasks automatically.
The model never directly changes authoritative state by emitting text.

Fix the semantics of runtime operations and registered adapter interfaces, not each
workflow, Git sequence, or agent role. An authorized general command/tool capability
may receive generated invocation content. Agent templates provide reusable context,
skills, tool/default policies, and executor selection; specialization is recorded and
frozen per admitted attempt. Native text, structured events, and artifacts remain
valid outputs. Adapters/interpreters record claims and proposals without requiring
PASS/FAIL/FIX markers or a Mitos response protocol from ordinary agents.

Separate structural graph revisions from changing execution facts. Bind proposals
to affected facts and current controller authority; bind attempts to immutable
specifications and input witnesses. Retain old evidence when work is superseded.
Represent retries as attempts and changed/additional work as new tasks. Keep the
dependency graph acyclic while allowing repeated planning and repair over time.

Persist expansion obligations and explicit scope sealing. Completion requires closed
required scope, accepted results, final verification, and settled or explicitly
transferred custody. An empty queue or a supervisor saying "done" is insufficient.
The ownership, limits, graph, decisions, and outstanding effects survive replacing
the controller; existing eligible work does not wait for that controller to launch it.

## Worked example (illustrative)

A goal asks for lower checkout latency, with an unchanged payment contract and a
benchmark requirement. Revision 1 contains an investigation task and an expansion
obligation. The worker returns ordinary analysis text and a trace artifact.

A planner proposes two experiments plus a comparison task consuming their exact
artifacts. A typed choice selects that concrete expansion; an atomic amendment adds
the tasks and resolves the original expansion obligation. The scheduler runs the
independent experiments. The comparison can open a new planning obligation under
the declared result-trigger policy; there is no prewritten jump table of all causes.

New evidence identifies a database issue. The next amendment introduces an implementation
and migration review using adapted agent templates, plus the mandatory benchmark.
The controller selects Git/workspace operations appropriate to the current repository
under existing capability grants. A failing benchmark generates a correction task and
a new verification target. It does not jump to an old accepted result or erase the
failure. The graph seals when no further expansion is outstanding; root acceptance
binds the exact final change, benchmark, review, and required obligation set.

## Alternatives and consequences

- A predeclared pipeline for every task keeps transitions explicit but forces authors
  to enumerate domain exceptions. Retain it as a policy option, not the foundation.
- An unconstrained model-owned loop makes topology flexible but hides admission,
  authority, replay, custody, and completion. Reject it as the runtime authority.
- A compulsory JSON/tag protocol for every agent simplifies extraction but makes
  workers serve framework interfaces. Keep typed records at the adapter/controller
  boundary and preserve native worker behavior.
- Requiring a decision-model call for every transition adds latency and expense to
  deterministic facts. Use models only where the policy needs semantic judgment.
- Treating graph revision as one global event counter causes unrelated output to
  invalidate decisions. Use structural revisions and explicit fact read sets.

The implementation needs common agent/application admission, graph transactions,
closure/acceptance, template/context adaptation, and replaceable controller services.
A general graph editor or more creation buttons alone cannot deliver this model.
Local trusted execution remains distinct from enforced confinement and spend control.
Existing histories and receipts remain compatible; no historical graph is invented.

## Owning contracts

- [Architecture](../system/01_ARCHITECTURE.md#adaptive-execution-composition): composition and layer boundaries.
- [Runtime](../system/02_RUNTIME.md#adaptive-work-graphs): identities, amendments, immutable attempts, iteration, and closure.
- [Orchestration](../system/03_ORCHESTRATION.md#adaptive-controllers): planning policies, templates, decisions, and replacement.
- [Verification](../system/04_STATE_AND_VERIFICATION.md#graph-results-and-acceptance): exact graph results and evidence reuse.
- [Interfaces](../system/05_INTERFACES_AND_INTEGRATIONS.md#adaptive-graph-and-controller-interface): semantic operations and native observations.
- [Delivery](../system/06_DELIVERY_AND_CONFORMANCE.md#adaptive-runtime-delivery-gates): G1–G5 and explicit implementation gaps.
