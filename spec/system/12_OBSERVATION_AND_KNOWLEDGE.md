# 12 – Observation and knowledge

## Observable state

Observations MUST identify source, time, execution incarnation, sequence or cursor,
and coverage. The system MUST distinguish observed state, inferred state, and
unknown state. A quiet terminal is not proof of a stalled agent; an absent heartbeat
is not proof of process exit.

The runtime SHOULD combine process activity, output, structured harness events,
progress checks, and declared health probes when available. Each adapter MUST
report what it cannot observe. Observation gaps MUST constrain unattended claims.
Late observations MUST NOT change a replacement's lifecycle or authority.

## Stalls and steering

Stall detection MUST use declared thresholds and coverage rather than an unexplained
model judgment. A detected stall MUST retain the relevant observations and recovery
policy. Recovery MUST have bounded attempts and preserve deadline and budget.

Steering MUST target a current execution identity and known interaction mode.
An adapter that cannot safely interpret the current prompt or modal state MUST
refuse ambiguous input. A sent command MUST NOT be reported as applied until an
appropriate receipt or observation establishes it. Raw terminal text MUST NOT be
used as a general reliable control protocol.

## Records and knowledge

Authoritative records, saved artifacts, and learned knowledge are distinct.
Records establish runtime facts. Artifacts are work products bound to attempts.
Knowledge consists of reusable claims or guidance derived from identified sources.

Knowledge MUST carry provenance, scope, revision, confidence or verification status,
and supersession. Proposed lessons MUST NOT mutate grants, authoritative metrics,
or accepted results. Conflicting claims MUST remain distinguishable until resolved;
a summary MUST NOT erase uncertainty.

Context assembly MUST select records relevant to the assignment and disclose stale
or contradictory information. A replacement supervisor MUST receive current runtime
facts before supplemental narrative. Private harness memory MUST NOT become the
only location of an assignment, pending decision, or unresolved external effect.

## Metrics

Runtime metrics MUST derive from committed records using documented definitions.
They MUST separate accepted outcomes from reported success, attempts from tasks,
and observed duration from censored or missing intervals. Missing measurements
MUST remain unknown rather than zero or an invented estimate.

Useful measures include verified progress, acceptance failures, recovery success,
correction effort, intervention time, decision precision, checkpoint quality, and
resource consumption. Reduced intervention is beneficial only alongside acceptable
outcomes and risk. Demo records MUST be excluded from operational evaluations.

## Inspection and retention

Clients MUST be able to trace a summary to its supporting records within their
permissions. Logs and exports MUST preserve task/attempt identity and result
provenance without exposing credentials. Retention MUST respect pending recovery,
active dependencies, and receipt deduplication before discarding supporting data.

A historical report is descriptive, not a conformance claim. Release acceptance
requires reproducible checks against the actual implementation revision, as defined
in [delivery and conformance](15_DELIVERY_AND_CONFORMANCE.md).
