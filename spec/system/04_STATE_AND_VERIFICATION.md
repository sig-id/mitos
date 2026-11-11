# 04 – Workspaces, verification, and knowledge

## Workspaces and artifacts

Workspaces MUST identify source, layout, owner, revision, and access policy. Shared
checkouts, worktrees, and copied batch directories MUST be distinguished. None alone
establishes confinement. Shared mutation obeys resource claims; layout changes MUST
NOT relocate live workers or discard changes. Conversation forks do not imply file
snapshots.

Local batch capture MUST copy tracked and non-ignored files, honoring nested ignore
rules, without modifying the source. Git metadata, Mitos data, and symlinks are
excluded; special files and submodules are explicitly refused. Capture records
paths, sizes, and hashes within 64 MiB / 10,000 files and initializes a private
repository. Unsafe paths or detected source changes MUST fail or produce an explicitly
incomplete result, never an apparently complete baseline.

After execution stops, including failure, cancellation, and timeout, harvest MUST
attempt to preserve partial work: baseline, binary-capable patch, change manifest,
archive, and output. Artifacts MUST bind identity, hash, size, and producing attempt.
Capture bounds and path safety also apply to harvest. Failure retains its cause,
raw workspace, and available output; it MUST NOT appear as an empty successful result.

Publication and acceptance are separate. Dependency inputs are private verified
copies bound to frozen witnesses under [runtime](02_RUNTIME.md). Acceptance rechecks
those witnesses. Builds, previews, scratch, logs, and exports MUST retain provenance;
sharing MUST NOT expose unrestricted host paths or confer launch authority. Deletion
MUST respect active dependencies, recovery holds, and published retention policy.

## Acceptance

Process exit, reported success, and accepted completion are distinct. Only the
authorized acceptance path may establish completion, binding task, attempt, exact
result digest, criteria revision, verifier, isolation posture, outcome, and artifacts.
Criteria precede judgment; amendments MUST be explicit, authorized, and revisioned.
Supervisors MUST NOT weaken checks silently. Verification cannot accept later edits.

Local execution MUST snapshot installed acceptance material before launch and
reconstruct the result from pristine baseline and harvested patch in a separate
directory. Verification uses saved material, not worker-modified checks. Changes
intersecting `oracle_paths` park acceptance for resolution. Commands within the
application directory rebase onto its snapshot; absolute paths inside arguments
remain an explicit adapter responsibility.

A verifier exit of zero records a local pass; other exits retain failure evidence.
The default 60-second acceptance timeout bounds reconstruction, verification, and
measurement within the execution deadline. Optional numeric measurements record
baseline, result, and delta even on failure; improvement is not itself acceptance.

Results MUST disclose what was checked and under which trust boundary. Independent
execution, inaccessible oracle material, deterministic checks, and human review
provide different assurance. A same-user verifier is not an inaccessible oracle;
worker explanations and supervisor confidence are not independent verification.

## Graph results and acceptance

A verifier MAY consume an immutable, not-yet-accepted candidate through the runtime's
`candidate_available` binding. Its verdict is evaluated against declared authority,
schema, evidence, and acceptance policy; recording that verdict MUST NOT imply an
infinite chain of verifier tasks. This does not relax substantive checks or turn an
unauthenticated model claim into acceptance.

A graph root result MUST identify its graph structural revision, root specification
and criteria revisions, sealed obligation set, and exact contributing task/attempt
results. The acceptance receipt MUST preserve the evidence manifest, verifier/policy
revision, findings, and any authorized scope discharges. Closure eligibility belongs
to [runtime](02_RUNTIME.md#expansion-sealing-and-completion); a passing verifier alone
cannot bypass an open planning scope or unresolved required work.

Acceptance policy MAY combine deterministic checks, independent agent assessment,
and human review. Each contribution MUST identify its method and trust boundary.
A decision-model routing answer is not a verifier verdict. If a model-backed verifier
is allowed, its task, fixed rubric, input artifact digests, and acceptance authority
MUST be declared before judgment; self-assessment MUST remain labeled as such.

Changes to a source artifact, selected input, criteria, or required membership MUST
invalidate pending acceptance for the old target. Previously accepted records remain
historical facts about their exact target; they MUST NOT be rewritten to endorse the
new state. Reuse requires an explicit provenance binding and policy check. A repair
creates a new candidate result, and checks consuming that candidate must run or be
revalidated against it. A supervisor cannot avoid a failing check by dropping its
node, marking it optional, or selecting an older passing result for newer changes.

Artifact references MUST describe what was actually produced. A shared directory,
conversation summary, or "latest output" pointer is not an immutable input binding.
Materialization into worker/verifier workspaces follows the existing bounded copy
and integrity contract. Published results MUST remain retained while live graph
witnesses, recovery operations, or unresolved effects reference them.

## Checkpoints and restoration

Checkpoints MUST bind assignment/criteria revisions, workspace/artifact digests,
accepted progress, relevant decisions, and outstanding operations. Scope exclusions
such as process memory, external systems, and model internals MUST be explicit.

Restoration creates a new recorded transition and retains prior history. It MUST
NOT rewind charges, reset budgets, resurrect revoked grants, erase uncertain effects,
or release resources held by a surviving process. Restored results MUST be revalidated
before acceptance or dependent execution. Recovery limits belong to the runtime.

## Observation and steering

Observations MUST identify source, time, incarnation, sequence/cursor, and coverage.
Observed, inferred, and unknown state MUST remain distinct. Quiet output is not
proof of a stall; a missing heartbeat is not proof of exit. Adapters MUST report
coverage gaps, which constrain unattended claims. Late observations cannot change
a replacement's lifecycle or authority.

Stall policy MUST declare thresholds and supporting signals, retain observations,
and use bounded recovery. Process activity, output, structured harness events,
progress checks, and health probes SHOULD be combined where available. Steering
MUST target current identity and a known interaction mode. Ambiguous prompts require
refusal; sent input is not proof of applied action.

## Knowledge and metrics

Records establish runtime facts; artifacts are work products; knowledge contains
reusable claims derived from identified sources. Knowledge MUST retain provenance,
scope, revision, confidence/verification status, and supersession. Proposed lessons
MUST NOT mutate grants, authoritative metrics, or accepted results. Contradictions
and uncertainty MUST survive summarization.

Replacement context MUST prioritize current runtime facts over supplemental narrative.
Harness memory MUST NOT be the sole location of assignments, pending decisions,
or uncertain effects. Context assembly MUST disclose stale or conflicting information.

Metrics derive from committed records with documented definitions. They MUST separate
tasks from attempts, acceptance from reported success, and observed duration from
censored/missing intervals. Missing values remain unknown; demo data is excluded.
Evaluation SHOULD measure verified outcomes, recovery, correction effort, intervention,
checkpoint quality, and consumption. Fewer questions alone does not prove autonomy.

Authorized clients MUST be able to trace summaries to supporting records without
exposing credentials. Retention respects dependencies, recovery, and deduplication.
Historical reports do not establish conformance; [delivery](06_DELIVERY_AND_CONFORMANCE.md)
requires reproducible checks against the implementation revision.
