# 06 – Agents and supervision

## Supervisory responsibility

A supervisor coordinates a durable responsibility, not merely a long-lived
conversation. The runtime MUST persist its assignment, current coordinator binding,
generation, inbox, child work, decisions, and limits independently of the harness.
At most one coordinator generation may authorize changes for that responsibility.

A coordinator MAY propose decomposition, placement, integration, recovery, and plan
amendments. Each proposal MUST name its observed state revision and authority.
The runtime MUST enforce admission, dependency, grant, and resource checks.
Already-admitted work MAY continue during coordinator replacement; work requiring
new authority MUST wait visibly.

Replacement MUST advance the coordinator generation, reject stale proposals and
mailbox settlement, and deliver the outstanding responsibility to the new executor.
Its context MUST include the goal, current task graph, accepted artifacts, pending
messages and decisions, active claims, remaining allowance, and unresolved effects.
Native history MAY supplement these records but MUST NOT override them.

## Project setup and delegation

Project analysis MUST produce a reviewable draft before changing saved policy or
starting a supervisor. A saved profile MUST carry a revision and distinguish
instructions from enforced constraints. Supervisor launch MUST bind the reviewed
goal, profile revision, execution layout, and limits.

Delegation MUST create actual managed work with recorded parentage and an assignment.
A background conversation or UI placeholder MUST NOT count as a worker. Worker
reports MUST identify their exact task, attempt, and process incarnation.
Reported success MUST pass the [acceptance contract](10_VERIFICATION_AND_CHECKPOINTS.md).

The current workspace permits one active supervisor per project. Its agent count
and disk reserve are launch checks; free-text Git, review, and collaboration rules
do not establish runtime enforcement. Product claims MUST preserve this distinction.

## Agent adapters

Adapters MUST declare discovery, launch, input/output, cancellation, continuation,
and structured question/approval capabilities. Unsupported operations MUST be
reported explicitly. The common lifecycle MUST NOT depend on one vendor's
conversation format or require an LLM for deterministic workloads.

Interactive agents MAY use native CLI configuration and credentials under the
trusted local posture. Session metadata MUST avoid copying secrets. Structured
hooks and events MUST retain source identity and distinguish observed intent from
an action actually admitted or completed by Mitos.

## Continuation

| Operation | Required meaning |
|---|---|
| Resume | Continue through a harness-supported conversation identity, with an explicit execution binding. |
| Fork | Create a distinct continuation and record its origin; state exactly which history and workspace are shared. |
| Handover | Give another executor a portable assignment and selected records, with new authority checks. |

A conversation fork MUST NOT imply a filesystem snapshot. A handover MUST NOT
claim exact transfer of hidden model state. Interrupted sessions require process
inspection and resource settlement before replacement. Executor changes preserve
the assignment and charges; they do not silently carry revoked authority forward.

## Collaboration

Topics provide discovery and discussion. Posts SHOULD be concise and link to work,
results, and decisions. Thread reads MUST be bounded; replies and follow cursors
MUST use stable identity and revision checks. Reading or acknowledging a topic
MUST NOT claim resources, approve an action, or complete a task.

Control messages use the durable inbox contract in
[communication and decisions](11_COMMUNICATION_AND_DECISIONS.md). The distinction
between discussion and control MUST remain visible to both agents and clients.
