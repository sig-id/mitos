# ADR 0006: Reusable agents and task-specific specialists

Status: accepted design, 2026-11-12; runtime implementation pending.
Contracts: [agent library](../system/07_AGENT_LIBRARY.md),
[controllers](../system/03_ORCHESTRATION.md#agent-templates-context-and-domain-adaptation),
[delivery](../system/06_DELIVERY_AND_CONFORMANCE.md#adaptive-runtime-delivery-gates).

## Context

The current graph UI can create a goal without a configured planner, call it
"Planning", and ask the user to add application steps. Workspace inventory/note
executors and per-step configuration do not deliver a goal-first agent OS.
A user should describe work; the system should plan it and provide agents capable
of doing it, including creating a new role when the library has no suitable match.

Chief Wiggum was inspected at sibling revision
`f11e476037819b9cd15ad00cc16bfd0956481aed`. This is a static source review, not a
runtime evaluation. Relevant sources and adaptations are:

| Chief Wiggum source | Retain | Change for Mitos |
|---|---|---|
| `spec/AGENT_SPEC.md`, `lib/core/agent-md.sh` | Declarative, reusable role instructions and context needs | JSON metadata plus ordinary Markdown; no required XML sections or reply tags. |
| `config/agents.json`, `lib/core/agent-config.sh` | Explicit defaults and bounded execution | Frozen revisions and effective allowances; no result-to-pipeline jump mappings. |
| `lib/agents/general/domain-expert.md` | Investigation grounded in domain knowledge | Generalist/researcher with selected sources; no claim of authority merely from the persona. |
| `lib/agents/product/plan-mode.md`, `product/system-architect.md` | Requirements awareness, design and decomposition | Planner proposes evolving work; architect is reusable expertise, not a mandatory phase. |
| `lib/agents/engineering/software-engineer.md` | Focused implementation and validation | No fixed PRD, loop count, branch/commit/push sequence or reply protocol. |
| `lib/agents/engineering/code-review.md`, `engineering/validation-review.md` | Actionable, evidence-based assessment | Review the bound candidate; retain uncertainty rather than an invented numeric confidence gate. |
| `lib/agents/engineering/test-runner.md`, `engineering/test-coverage.md` | Reproducible checks and test design | One adaptable tester; scope checks to the assignment and actual capabilities. |
| `lib/agents/general/documentation-writer.md`, `general/task-summarizer.md` | Clear documentation and synthesis | May create or update requested documents; do not assume success or require inherited private reasoning. |
| `lib/agents/system/failure-resolver.md` | Investigate failures and propose useful recovery | New recovery work under runtime policy; no automatic scope reduction or fixed pipeline restart. |

The adapted packages retain per-definition provenance and the upstream MIT notice.
Chief Wiggum shell overrides and workflow implementations are not imported.

## Decision

Make definitions library records, independently of running instances and application
personas. A definition captures expertise, knowledge, skills, context and execution
requirements. A Task binds a revision and specialization; an Attempt freezes its
resolved runtime, context and authority. A shared definition does not share a process,
private conversation, mutable workspace or permission grant.

Ship a small default library and an eligible generalist base. The planner reuses or
specializes a definition, or creates a graph-scoped specialist when no role fits.
Generation and its new tasks commit atomically, with bounded quotas and provenance.
Promotion to a reusable project/installation entry is separate from ordinary work.
Native output remains supported; only explicit controller interfaces propose graph
mutations. The runtime owns admission, capacity, recovery and acceptance.

Bootstrap planning from a configured compatible runtime, not from a user-authored
work graph. Missing a specialist prompts creation; missing capacity prompts queuing;
missing a runtime or credential prompts an accurate setup state. These are different
conditions and must not all become an empty "Planning" node.

## Alternatives and consequences

A fixed named-role catalog is useful for discovery but cannot cover arbitrary work.
A free-text persona alone omits compatibility, knowledge provenance and replay.
A persistent process per library entry confuses reusable expertise with execution
and needlessly consumes resources. Importing Chief Wiggum's pipeline/result grammar
would preserve rigidity the adaptive runtime is intended to remove.

The library therefore requires validated immutable revisions, scoped discovery,
compatible native runtime adapters, context assembly, transactional specialization,
and observable planning/selection. Authored definitions do not implement those
mechanisms. The current goal UI and registered application adapter remain incomplete
until the library-driven journey passes G4/G5. Existing graphs must not start running
merely because definitions were installed or a browser was opened.
