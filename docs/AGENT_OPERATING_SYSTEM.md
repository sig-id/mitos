# Mitos agent operating system

Design direction agreed on 2026-11-11.

Mitos will center on durable work executed by replaceable agents and deterministic
programs. Services define ongoing responsibilities, tasks define concrete work,
and processes execute it. The operating system governs their resources, authority,
communication, and durable state.

An agent framework helps assemble agents. An agent operating system provides a
common execution environment whose guarantees survive replacement of those agents.
This is a runtime above the host operating system, not a replacement for its kernel.

This design establishes the conceptual model. It does not claim these guarantees
are all implemented or replace existing API contracts. The
[implementation status](../spec/00_IMPLEMENTATION_STATUS.md),
[interactive workspace contract](INTERACTIVE_WORKSPACE.md), and
[architecture specification](../spec/01_ARCHITECTURE.md) describe the current
implementation boundaries and earlier target contracts. Future implementation
work must reconcile those contracts with this direction explicitly.

## Durable work and replaceable executors

Work is the lasting identity. A task such as fixing checkout must survive an agent
crashing, a different harness taking over, the machine restarting, or the operator
returning later. Conversations contribute to the history but cannot be the only
place the assignment, progress, or outstanding decisions exist.

A project groups goals, tasks, resources, and services. A goal describes an outcome
and its constraints; tasks organize the concrete work toward it. Persistent agent
names and roles may be useful, but correctness must not depend on a particular
agent retaining private conversational memory.

## Six primitives

| Primitive | Meaning | Required guarantee |
|---|---|---|
| Task | Work with inputs, constraints, and a completion condition | Identity and progress survive individual execution attempts. |
| Process | One execution working on a task, using an LLM harness or deterministic program | Explicit lifecycle, parentage, cancellation, and resource accounting. |
| Resource | A workspace, artifact, tool, model endpoint, or external service | Addressable identity, access rules, ownership, and limits. |
| Grant | Authority to perform particular operations | Enforced scope, delegation limits, expiry, and revocation. |
| Channel | Communication between processes, services, humans, and external sources | Explicit delivery, acknowledgment, and cancellation semantics where applicable. |
| Record | Durable facts about decisions, effects, and results | Recoverable system state and traceable evidence. |

A process here is a managed execution, not simply a host PID. It may own several
host processes. The existing Task, Run, and agent-session identities need an
explicit mapping into this model; they must not be collapsed merely by renaming
them. A task can have multiple attempts, including attempts using different executors.

A grant is authority, distinct from the existing specification's packaged
Capability, which bundles data, skills, and tools. Installing such a package must
not implicitly grant everything that it requests.

Records reconstruct known system state. Replaying records does not mean repeating
external effects or reproducing an LLM's reasoning.

## Role executor and activation

These are independent dimensions of execution:

| Dimension | Question | Examples |
|---|---|---|
| Role | What responsibility does this execution have? | Supervisor, worker, verifier. |
| Executor | How is that responsibility performed? | LLM harness, deterministic program. |
| Activation | What causes work to become ready? | Human request, schedule, external event, another task's completion. |

A scheduled task can invoke an LLM. An external event can start a deterministic
program. A supervisor can reason with an LLM or apply fixed coordination rules.
Scheduling and event handling do not imply a particular executor.

## Supervisors run on the operating system

A supervisor is a process with a supervisory role, executing an assignment to
coordinate work toward an outcome. It uses the same six primitives as a worker:
its task specifies the assignment, resources provide context and tools, grants
authorize delegation and control, channels carry assignments and results, and
records preserve decisions and effects.

The supervisor proposes plans, creates subtasks within its authority, allocates
available budgets, and adapts to results. The kernel admits and executes those
operations under enforced limits. A request for five workers can therefore admit
fewer workers when capacity or grants require it. Delegating work cannot expand
the parent's authority or create an unbounded new budget.

The supervisor is not the operating system. Its context can fill up, its reasoning
can be wrong, and its process can fail. Ownership, permissions, task state, and
recorded effects must remain valid independently of it. A replacement receives
the durable assignment, task graph, outstanding decisions, and result evidence.

## Services preserve ongoing responsibilities

A service is a first-class composition above the six primitives. A task describes
a particular piece of work; a service describes an ongoing responsibility and
the rules for creating or maintaining that work, including while no process runs.

For example: whenever a security advisory is published, assess whether the
project's repositories are affected.

| Service component | Security advisory example |
|---|---|
| Activation rule | An advisory arrives from an accepted source. |
| Task template | Assess affected repositories and produce findings. |
| Execution specification | Run the selected program or agent application. |
| Operating policy | Permissions, budget, concurrency, retries, and overlapping-event behavior. |

```text
External source / clock / human
              |
      Event received on a channel
              |
      Service activation rule
              |
          Durable task
              |
       Scheduled process
              |
    Result, evidence, further work
```

The channel transports an event. The activation rule decides whether it creates
work. The scheduler decides when that work can execute. The executor performs it.
External connectors normalize source events into this flow; no particular
transport, including AMQP, is required by the model.

Activation needs explicit duplicate-delivery, missed-schedule, overlap, and
backpressure policies. Receiving the same PR event twice should not accidentally
create duplicate work. Receiving a hundred advisories must not bypass concurrency
limits. An external event supplies input, not authority.

A supervisor service preserves a responsibility such as keeping a project moving
toward a goal. A supervisor process is its current executor. Tasks preserve the
actual work and progress. Services may run continuously, activate periodically,
or wait for events, and their executors can be replaced.

## System layers

| Layer | Responsibilities |
|---|---|
| Kernel | Execution lifecycle, scheduling, resource ownership, permission enforcement, durable records, and communication. |
| System services | Supervision, memory retrieval, verification, credential brokering, triggers, and notifications. |
| Applications and interfaces | Packaged workflows, coding teams, research and operational applications, Studio, CLI, and handheld interfaces. |

Keep the kernel small and its enforcement deterministic. A supervisor decides
whether another attempt is worthwhile; the kernel enforces the spending limit.
A verification service evaluates a result; the kernel records which evidence
supports completion. Credential brokering can be a privileged system service,
but its authority boundary must be enforced by the execution environment.

Applications package behavior and configuration against these contracts. Interactive
and automated interfaces operate on the same underlying managed objects.

## Implementation gaps and priorities

Mitos already has documented foundations in durable tasks, execution records,
scheduling, services, checkpoints, decisions, and workspace coordination. The
transition requires consistent guarantees across those mechanisms.

1. **Unify interactive and automated execution.** Establish common identity,
   lifecycle, permissions, and accounting beneath agent sessions and batch work.
   A terminal becomes an interface to a managed process.
2. **Enforce authority and resource boundaries.** Integrate isolation, credential
   brokering, resource limits, and bounded delegation. The documented trusted
   local provider does not enforce the full target boundary.
3. **Make communication and waiting durable.** Provide explicit delegation,
   result waiting, child cancellation, and decision delivery. Waiting should
   release execution capacity while preserving state. Topic discussions remain
   useful but cannot substitute for execution-control semantics.
4. **Recover with awareness of external effects.** Use operation identities,
   receipts, and reconciliation. If a deployment request may have succeeded before
   a crash, represent an unknown outcome rather than blindly submitting it again.
   Restoring files alone does not undo external actions.
5. **Manage knowledge beyond session history.** Separate authoritative task state,
   saved artifacts, and fallible learned knowledge. Give memory provenance, scope,
   revision, and supersession so replacement executors can reconstruct useful
   context without treating every remembered claim as fact.

Prioritize the shared execution model and enforced boundaries, then demonstrate
the complete lifecycle in one workflow: create a task, delegate work, exhaust a
worker budget, replace the worker with another harness, restart Mitos, reconstruct
remaining work, verify the result, and request a human decision where authority
is insufficient. Completion requires durable continuity without duplicate effects
or lost resource ownership, rather than only a successful final agent response.
