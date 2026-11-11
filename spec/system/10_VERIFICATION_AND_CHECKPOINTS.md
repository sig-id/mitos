# 10 – Verification and checkpoints

## Acceptance

Process exit, worker-reported success, and accepted task completion are distinct.
Only the authorized acceptance path may establish completion. It MUST bind the
task, attempt, exact result digest, criteria revision, verifier identity, isolation
posture, outcome, and supporting artifacts.

Criteria MUST be declared before the result is judged. Amendments MUST be explicit,
authorized, and revisioned; a supervisor MUST NOT silently weaken checks to obtain
a pass. Verification against one result MUST NOT accept later workspace changes.
Missing or contradictory evidence MUST remain visible.

## Local verification contract

Before execution, Mitos MUST snapshot the installed application's acceptance
material. After execution, it MUST reconstruct the result from the pristine
baseline and harvested patch into a separate directory. The verifier MUST use
that reconstructed result and saved acceptance material.

Changes intersecting declared `oracle_paths` MUST park acceptance for resolution.
Verifier commands inside the application directory MUST be rebased to its snapshot;
absolute paths embedded in arguments remain an explicit adapter responsibility.
A successful verifier exit records a local pass; other exits record failure with
output and cause. This is separate execution, not proof of adversarial isolation
from a worker running as the same host user.

The acceptance timeout defaults to 60 seconds and MUST bound reconstruction,
verification, and measurements within the overall execution limit. Optional numeric
measurements MUST record baseline, result, and delta even if the predicate fails.
A measurement change alone MUST NOT substitute for the acceptance predicate.

## Verification strength

The result MUST disclose what was checked and under which trust boundary.
Independent execution, inaccessible oracle material, deterministic checks, and
human review are different assurance mechanisms. A same-user local verifier MUST
NOT be described as an inaccessible oracle. A worker's explanation or supervisor's
confidence MUST NOT be represented as independent verification.

Strict unattended claims require actual observation, confinement, bounded recovery,
and adequate checks for the work being delegated. If these requirements cannot be
met, admission MUST refuse that posture or request an explicitly narrower action.

## Checkpoints

A checkpoint MUST preserve enough information to reconstruct its declared scope:
assignment and criteria revisions, workspace/artifact digests, accepted progress,
relevant decisions, and outstanding operations. It MUST state what is excluded,
such as process memory, external systems, or native model internals.

Restoration MUST create a new transition and retain prior history. It MUST NOT
rewind charges, reset budgets, resurrect revoked grants, remove unresolved effects,
or release resources still held by a surviving process. Restored files MUST be
revalidated before new acceptance or dependent execution.

## Acceptance evidence and product claims

Conformance evidence MUST identify the tested revision, scenario, result, and
limitations. Historical test counts or sample JSON MUST NOT establish current
conformance. Verification failures are useful records and MUST remain inspectable.

Evaluation SHOULD measure accepted outcomes, recovery failures, corrective work,
human intervention, and resource consumption. A low question count or high process
exit-success rate alone MUST NOT establish autonomous completion.
[Delivery and conformance](15_DELIVERY_AND_CONFORMANCE.md) defines release gates.
