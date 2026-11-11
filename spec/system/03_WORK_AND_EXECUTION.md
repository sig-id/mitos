# 03 – Work and execution

## Identity

A Task MUST retain its assignment, inputs, constraints, completion criteria,
parentage, and accepted progress across attempts. An Attempt binds one execution
to that task. Existing Run IDs identify attempts and MUST remain stable.
A Process identifies managed execution, including its host and incarnation;
a Session identifies its interaction/history binding.

Each launch MUST record task, attempt, process, session when applicable, executor,
authority generation, workspace revision, and launch receipt. Unbound historical
sessions MUST remain explicitly unbound; migration MUST NOT fabricate task lineage.
Native resume that starts execution MUST record a new execution binding.

## Admission

Admission MUST validate the current assignment and policy revision, caller grant,
dependencies, provider capabilities, budget, and resource availability. Its durable
commit MUST include the task transition, attempt identity, reservations, claims,
request receipt, and dispatch outbox entry. Either the entire admission is visible
or none is. No process may start from an uncommitted proposal.

A queued attempt receipt MAY exist before execution admission. Listing queued
work MUST distinguish it from admitted or running work. Proposal approval alone
MUST NOT be reported as launch success.

## Lifecycle

| Stage | Required meaning |
|---|---|
| Queued | Assignment exists; execution may still await dependencies or capacity. |
| Admitted / starting | Authority and resources are reserved; launch outcome may be pending. |
| Running | Exact process incarnation has been established. |
| Waiting | Wake condition is durable; retained processes and resources remain accounted for. |
| Reported complete | Executor claims an outcome; acceptance has not necessarily occurred. |
| Accepted | Authorized verification passed against the exact result and criteria revision. |
| Failed / interrupted / cancelled | Execution ended or is unresolved; recovery and resource settlement may remain. |

These are semantic distinctions, not an instruction to rename existing wire enums.
Task outcome, attempt outcome, process exit, and resource release MUST remain
separate facts. A failed attempt awaiting retry MUST NOT terminate its task.
A new attempt MUST preserve prior outcomes and accumulated charges.

## Cancellation and replacement

Cancellation MUST identify scope, including descendants, and persist intent before
signalling processes. Completion of a stop request requires verified process exit
or enforceable revocation. Claims MUST remain held while a process can still write.
Parent cancellation MUST settle children or explicitly detach them under policy;
closing a client MUST NOT imply cancellation.

Replacement MUST advance the relevant execution generation and bind a new
incarnation. Late reports from the old process MAY be retained as observations but
MUST NOT complete or mutate its replacement. Fencing requests does not stop file
writes: [authority and resources](05_AUTHORITY_AND_RESOURCES.md) governs safe release.

## Output and interaction

Output MUST remain inspectable after process exit and server restart, within
published retention limits. A slow or disconnected viewer MUST NOT block a PTY.
Interaction commands MUST target the actual session and process incarnation.

Terminal bytes, structured control operations, and task results are different
channels. Input delivery does not prove application of a command or completion
of a task. Ambiguous raw terminal input MUST NOT be automatically retried.
[API and compatibility](14_API_AND_COMPATIBILITY.md) defines stream behavior;
[agents and supervision](06_AGENTS_AND_SUPERVISION.md) defines continuation.
