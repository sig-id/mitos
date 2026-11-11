# 05 – Authority and resources

## Grants

A Grant MUST identify the issuing authority, subject, permitted operations,
resource scope, validity period, delegation bounds, and applicable budget. Every
admission and privileged mutation MUST validate it. Delegation MUST preserve or
narrow these limits; replacing an executor MUST NOT create fresh authority or
reset spending.

Approval of one decision MUST grant only its declared action and scope. Installing
an application, reading a prompt, receiving a message, or joining a topic MUST NOT
confer authority. Credentials MUST NOT be copied into ordinary task metadata,
artifacts, or transcripts. Where brokering is required, the broker MUST bind use
to an admitted operation and account for consumption.

Revocation MUST fence new control operations immediately. Existing process access
MUST be terminated or revoked by the provider before resources are reassigned.
A rejected stale API call is not proof that the process can no longer write files.

## Resource ownership

Resources MUST have stable identity and explicit access modes. Claims MUST record
owner, task/attempt, generation, scope, and release conditions. Conflicting claims
MUST be rejected or queued deterministically. Claims that span admission MUST be
committed with reservations and the launch receipt.

A timeout, disconnected client, silent worker, or unavailable host MUST NOT alone
release a claim. Release requires confirmed stop, enforceable access revocation,
or a provider guarantee that makes concurrent access safe. Cancellation and
recovery MUST include processes that still hold resources after their task ends.

## Budgets and capacity

Reservations MUST bound admitted consumption; metered charges MUST survive retry,
replacement, and checkpoint restoration. Unknown consumption MUST remain explicit.
Budget policy MUST specify enforcement and exhaustion behavior. Estimated token
costs or a prompt saying “stop after this amount” MUST NOT be represented as a
hard limit.

Concurrency, memory, disk, model spend, and external-service limits are separate
resources. Releasing a scheduler slot while waiting MUST NOT erase a running VM,
child process, retained workspace, or outstanding reservation from accounting.

## Provider admission

A provider MUST advertise supported execution, observation, isolation, revocation,
and recovery capabilities. The runtime MUST reject a requested posture it cannot
enforce; it MUST NOT silently downgrade to a trusted local process. Launch policy
MUST be frozen into the attempt and explain admission or refusal.

| Posture | Required interpretation |
|---|---|
| Trusted local process | Runs with the operator's host authority; environment hygiene is not a sandbox. |
| Confined worker | Provider enforces declared filesystem, network, credential, and resource restrictions. |
| Remote execution | Placement is explicit; isolation and recovery depend on the named provider. |
| Unattended operation | Actual authority, observation, acceptance, and recovery gates are satisfied. |

The current local provider is trusted same-user execution. Batch jobs isolate
HOME/TMPDIR and avoid inheriting provider credentials, while interactive agents use
native configuration. Neither path establishes host confinement or inaccessible
verification data. Free-text project rules are instructions, not enforcement.

## Workspace access

Shared checkouts and separate worktrees MUST state their ownership and conflict
policy. A worktree is not a security boundary. Strict exclusive mutation requires
provider enforcement or confirmed process settlement; topic messages and voluntary
agent cooperation are insufficient. [Workspaces and artifacts](09_WORKSPACES_AND_ARTIFACTS.md)
owns snapshot and result integrity.
