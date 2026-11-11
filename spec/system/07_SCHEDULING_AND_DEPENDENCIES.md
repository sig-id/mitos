# 07 – Scheduling and dependencies

## Work admission

A work pool governs queued tasks, execution capacity, and scheduling policy.
Scheduling MUST select only eligible tasks and reserve capacity atomically with
admission. Selection MUST be deterministic for the same committed state.
Queue position, dependency readiness, reservation, and actual launch MUST be
separate observable facts.

Supported policies MAY include FIFO, LIFO, priority with aging, and earliest
deadline. Each policy MUST specify tie-breaking and starvation behavior. Pool
reservations MUST NOT exceed available capacity or allow one task to acquire
conflicting resources through separate concurrent admissions.

The current local capacity is two workers. Configuration or UI labels MUST NOT
imply capacity or enforcement the provider does not supply.

## Dependencies

A dependency MUST identify its upstream task, completion condition, revision, and
whether failure blocks the dependent. Groups MUST declare all, any, or k-of-n
semantics. Readiness MUST be evaluated against committed outcomes, not worker
messages or the current appearance of a terminal.

The existing unguarded dependency means an upstream terminal outcome; it MUST NOT
be silently changed to success-only. CLI `--after` requires done and
`--after-failure` requires failed. A failed attempt with a pending retry is not a
terminal task outcome. Cycles MUST be rejected before admission.

For any/k-of-n groups, the selected witnesses MUST be stable, using terminal event
order and a deterministic tie-break. Losing upstream tasks MUST NOT be cancelled
unless the declared policy authorizes it. A later result MUST NOT change the
inputs of an already-admitted dependent.

## Data dependencies

An artifact dependency MUST bind the upstream task, attempt, event, artifact
identity, content hash, and size. Admission MUST freeze these witnesses. Materialized
inputs MUST be verified before execution and checked against the same witnesses
when used for acceptance.

The local contract allows 1–32 artifact names per edge, with a total of at most
128 files and 64 MiB. Inputs MUST be private copies with safe relative paths;
missing, changed, oversized, or escaping artifacts MUST block execution.
Read-only file modes are hygiene, not a sandbox against the same host user.

## Waiting

A wait MUST persist its predicate, relevant revision/cursor, deadline, cancellation
scope, and retained resources. Checking the predicate and registering the wake
condition MUST be atomic, or use a cursor-based replay with equivalent no-loss
behavior. A result arriving during registration MUST wake the task.

Wake delivery MAY repeat; application MUST be idempotent. Reawakening MUST recheck
current authority and resource eligibility before admission. Waiting MAY release
execution capacity only after accounting for surviving processes and claims.
A sleeping model request with an active shell or VM is not zero-cost work.

## Cancellation and fairness

Queue cancellation MUST prevent new admission. Cancellation after dispatch MUST
follow process settlement and retain unsafe claims. Dependency changes MUST be
revisioned and MUST NOT retroactively rewrite the evidence of admitted work.

Services feed this scheduler rather than bypass it. Service priority affects queue
selection; it MUST NOT evade capacity, grants, dependency checks, or safe replacement.
See [services and events](08_SERVICES_AND_EVENTS.md).
