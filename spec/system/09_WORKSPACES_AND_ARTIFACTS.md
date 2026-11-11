# 09 – Workspaces and artifacts

## Workspace modes

A workspace MUST identify its source, execution layout, owner, revision, and access
policy. Shared checkouts, separate Git worktrees, and copied batch workspaces have
different behavior and MUST be identified explicitly. A worktree or copied directory
MUST NOT be described as host confinement.

Shared mutation MUST obey resource claims and the project's integration policy.
A conversation fork MUST NOT imply a filesystem snapshot. Changing layout MUST
not silently relocate live workers or discard their changes.

## Batch snapshot

The local batch path MUST copy eligible source files into a private execution
workspace without modifying the source. Git-aware selection MUST include tracked
and non-ignored files, including nested ignore rules. Repository metadata, Mitos
data directories, and symlinks MUST be excluded. Unsupported special files and
submodules MUST be refused explicitly.

The snapshot MUST record paths, sizes, and content hashes and enforce the current
64 MiB / 10,000-file bound. Unsafe paths and source changes detected during capture
MUST fail or produce an explicitly incomplete result, never an apparently complete
baseline. Execution MUST initialize its own repository for change capture.

## Harvest

After execution stops, including failure, timeout, and cancellation, the provider
MUST attempt to preserve partial work. The local result consists of the baseline,
binary-capable patch, change manifest, workspace archive, and command output.
Artifacts MUST have stable identities, hashes, sizes, and producing attempt.

Harvest MUST enforce the same bounds and path safety as capture. A harvest failure
MUST preserve its cause and retain the raw workspace and available output. Failure
to capture changes MUST NOT be presented as an empty successful result.

Artifact publication and acceptance are separate transitions. A worker MAY produce
an artifact that is rejected or remains unverified. Retention policy MUST identify
what remains available for recovery and dependency consumers before deletion.

## Dependency inputs and verification

Materialized upstream artifacts MUST be private verified copies bound to frozen
witnesses. Symlink traversal and path escape MUST be refused. Hashes MUST be checked
again when an artifact is used as acceptance evidence. See
[scheduling and dependencies](07_SCHEDULING_AND_DEPENDENCIES.md).

Acceptance MUST reconstruct the exact result in a separate working directory from
the pristine baseline and harvested changes. Verification MUST use the saved
criteria and oracle snapshot, not worker-modified checks. Details belong to
[verification and checkpoints](10_VERIFICATION_AND_CHECKPOINTS.md).

## Managed outputs

Builds, previews, scratch files, logs, and exported reports MUST retain ownership
and provenance. Sharing an output MUST NOT expose an unrestricted host path or
confer launch authority. Removing a client view MUST NOT delete a managed artifact.
Deletion MUST respect active tasks, recovery holds, and dependency references.

Checkpoints and archives preserve files; they do not reverse external effects,
charges, approvals, or resource custody. Restoration MUST create a new recorded
revision and revalidate dependent claims and results.
