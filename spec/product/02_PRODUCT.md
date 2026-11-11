# 02 – Product

Status: normative product contract, accepted 2026-11-11.
[Specification authority](../README.md) defines precedence. The
[implementation ledger](../system/IMPLEMENTATION_STATUS.md) records current coverage.
Decision: [ADR 0004](../ADRs/0004_DURABLE_AGENT_OS.md).

## 1. Purpose

Mitos MUST provide an operating environment in which people can assign work to
agents and deterministic programs, inspect progress, replace executors, and obtain
verified outcomes without losing work, authority boundaries, or accountability.
Services MUST preserve ongoing responsibilities between executions. Tasks MUST
preserve concrete assignments across retries, harness changes, and runtime restart.

The primary user is an operator managing projects and ongoing responsibilities.
The same runtime MAY support teams and hosted deployments when identity and
isolation contracts are implemented. Commercial applications MAY sell completed
work under [Facilitator](../system/08_FACILITATOR.md); commerce is not a prerequisite
for a usable local agent operating system.

## 2. Objects users must be able to distinguish

| Object | User question |
|---|---|
| Project / Goal | What outcome are we pursuing, and under which constraints? |
| Service | What ongoing responsibility remains active between executions? |
| Task | What work is assigned, waiting, reported, or accepted? |
| Attempt / Process | Who is executing it now, where, and what happened to previous executors? |
| Session | Where can I inspect or interact with this execution? |
| Resource / Grant | What does it own, and what is it allowed to do? |
| Decision / Evidence | What needs my judgment, and what establishes the result? |

The UI MAY combine these objects in a compact view, but MUST NOT conflate a worker
exit or success message with acceptance. Existing Mitos Runs remain attempts;
retry MUST NOT appear as an unrelated assignment. A durable agent means a lasting
responsibility with replaceable executors, not an immortal conversation.

## 3. Core journeys

### Assign and coordinate

The operator MUST be able to open a project, review context and policy, set an
explicit goal, and launch a supervisor or direct worker. Analysis MUST NOT silently
start implementation or save unreviewed policy. The workspace MUST expose actual
agent interaction, task ownership, bounded topic discovery, evidence, and decisions.
[Workspace](WORKSPACE.md) defines the detailed interaction contract.

The supervisor MAY propose delegation and plans. Runtime admission MUST enforce
scope, budget, claims, and capabilities. Users MUST be able to distinguish
cooperative instructions from enforced restrictions.

### Leave and return

Closing or disconnecting a client MUST NOT cancel work. On reconnect, the client
MUST show authoritative state and retained output, including truncation or unknown
outcomes. Unsupported process survival after runtime restart MUST be disclosed.
Reattachment MUST NOT create another execution merely because a response was lost.

### Replace and recover

The operator or authorized policy MUST be able to replace an executor while
preserving assignment, accepted progress, pending decisions, remaining budget,
claims, and unresolved effects. Replacement MUST be visibly linked to prior work.
If the old execution may still affect shared resources, replacement MUST wait for
safe settlement rather than hide the uncertainty.

### Review and decide

Completion MUST expose the result revision, verification evidence, and limitations.
The operator MUST be able to inspect failures and partial results. Decision cards
MUST identify the proposed action, scope, consequences, relevant evidence, and
available choices. Reading or dismissing a notification MUST NOT resolve a card.
No interface may grant authority merely by making an operation convenient.

### Maintain an ongoing responsibility

Services MUST expose activation, overlap, missed-run, retry, and shutdown policy.
Operators MUST be able to inspect why work is ready, queued, skipped, blocked, or
stopped. A disabled service MUST NOT acquire new execution authority through a
late delivery. Dependencies and resource waits MUST have understandable reasons.

## 4. Clients and deployment

Browser, CLI, desktop, mobile, and handheld clients MUST address the same managed
objects. Supported operations MAY differ by capability and permission. A native
mobile app or desktop shell is optional; responsive web and CLI remain valid clients.
Switching clients MUST NOT require migrating a running agent.

The product MUST distinguish local execution, remote client access, SSH placement,
paired remote runtime execution, and provider isolation. A relay provides
connectivity; it does not host the user's agent or establish a sandbox. Remote
clients MUST show the host and authority scope of consequential actions.

## 5. Acceptance gates

The [implementation plan](../system/09_IMPLEMENTATION_PLAN.md) owns measurable
milestones. Product conformance requires an end-to-end lifecycle: assign a Task,
delegate, wait, exhaust a bounded allowance, replace a worker, restart the runtime,
recover remaining work, independently verify the result, and obtain a human
decision when authority is insufficient. It MUST preserve identity and evidence
without duplicate effects or lost claims.

An unattended-execution claim additionally requires enforced provider boundaries,
independent acceptance, bounded recovery, and representative evidence under
[Autonomy](../system/16_AUTONOMY.md). Instrumentation alone does not satisfy it.
A commercial launch additionally requires identity, isolation, proof, metering,
and settlement under the Facilitator contract. Neither gate is implied by local
runtime conformance or by an agent reporting success.

## 6. Scope and non-goals

Mitos MUST preserve Go runtime contracts and existing local data through migration.
A copied IDE feature set, marketplace, native mobile app, replicated control plane,
and transparent process migration are not requirements of the first OS milestone.
The architecture MUST allow richer clients and providers without making their
presentation, transport, or model choice the source of authoritative work state.
