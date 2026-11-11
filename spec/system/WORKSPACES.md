# Workspace change and acceptance records

Status: Normative local behavior and API contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

The local command lifecycle now captures the input, runs in an independent Git
working tree, harvests the filesystem after stop, and verifies a reconstruction
of the recorded change. All client surfaces use this lifecycle.

## Intake

Git selects tracked files and untracked, non-ignored files. Nested ignore rules,
negation, and repository-local exclude rules apply. Tracked ignored files remain
included. Host global ignore/configuration settings are neutralized. A plain
directory uses an external temporary Git index to evaluate its `.gitignore`
files; the source receives no `.git`, index changes, commits, or branch updates.

Intake excludes symlinks, Git metadata, and the configured Studio data directory.
Special files and nested repository/submodule entries are refused pending an
explicit materialization policy. Non-UTF-8 and escaping paths are refused.
Preflight checks the complete selected size and count against 64 MiB and 10,000
files before writing destination contents. A refusal names up to five largest
contributors. Reads remain bounded if a source file grows after preflight.

The captured bytes and executable modes form the baseline. Snapshot paths,
sizes, modes, and SHA-256 digests are recorded in `baseline.json`. The private
`history.git` contains the baseline commit. The worker gets its own `.git`, with
separate objects, index, no remote, no inherited hooks, and a local Mitos commit
identity. Its commits, resets, or ignore-file edits do not change what the
control plane treats as the baseline.

Git subprocesses run without inherited credentials, global/system configuration,
fsmonitor helpers, signing, templates, or external diff/text-conversion helpers.
Blob construction uses raw stdin and never invokes a working-tree clean filter.
Git objects/refs use `core.fsync=all` before their identities are recorded.

## Harvest

After the command stops, the control plane scans actual filesystem entries,
excluding `.git`. It does not use the worker's index or ignore rules. It produces:

| Artifact | Contents |
|---|---|
| `output.txt` | Combined stdout/stderr, capped at 2 MiB |
| `baseline.json` | Input manifest and content fingerprint |
| `changes.patch` | Full-index Git patch, including binary data and mode changes |
| `changes.json` | Baseline/result commits, fingerprint, and added/modified/deleted paths |
| `workspace.tar.gz` | Result contents, executable modes, and symlink entries |
| `acceptance.txt` | Output from the independent acceptance command, when run |
| `measure-baseline.txt`, `measure-result.txt` | Optional numeric measurement outputs |

Rename detection is disabled so both the removed and added paths remain explicit
for oracle checks. Generated files are included even if the worker ignored them.
Result capture is bounded by the same size/count ceiling as intake. Escaping
symlinks can be recorded as patch/archive evidence but cannot be admitted into
the verification tree. Archives omit Git metadata and empty directories.

Failure and timeout still harvest. A cancellation request moves running work to
`stopping`; it becomes `cancelled` after the process group is stopped and bounded
harvest finishes. Queued or already parked work can close immediately. The cleanup
window is at most 60 seconds beyond the execution deadline. A harvest error
records `harvest_failed` and leaves output and the raw workspace available;
it never claims a complete change artifact. SIGKILL/power-loss recovery cannot
recover output that was only buffered in memory, and does not yet finish a
partially recorded harvest automatically.

## Acceptance

```json
{
  "acceptance": {
    "cmd": ["/absolute/path/to/app/verify.sh"],
    "oracle_paths": ["tests/acceptance", "verify-config.json"],
    "timeout_ms": 60000,
    "measure": ["/absolute/path/to/app/measure.sh"]
  }
}
```

`cmd` is required; `oracle_paths` and `measure` are optional. `timeout_ms` defaults
to 60,000 and caps reconstruction, predicate, and measurements together within
the run deadline. The old argv-array acceptance syntax still loads; serializing
an application emits the object shape.

Oracle paths are literal workspace-relative paths or directory prefixes, not
globs. `.` protects the whole workspace. Added, deleted, content-changed, and
mode-changed paths all participate. An intersection records `oracle_tampered`,
records an escalation Decision, and parks an external judgment card before the
predicate runs. Editing `.gitignore` cannot hide an intersection.

Before the worker starts, the installed application's files are copied into an
oracle directory outside its working tree. Acceptance runs from that saved copy.
An executable inside the installed application directory is rebased onto this
copy; relative script paths resolve there. Absolute paths embedded in command
arguments are not rewritten; authors must avoid pointing these at mutable
installed files. Changes to the saved oracle are checked before and after the
predicate and produce `oracle_tampered` when detected.

Verification creates another private Git working tree from the pristine baseline,
applies the harvested patch to its index and files, and compares the complete
content/mode fingerprint to the harvest. A mismatch refuses verification. The
verifier's stdin `workspace` and `MITOS_WORKSPACE` refer to this reconstructed
result, with a separate home and temporary directory. It can build and run tests
without modifying the worker's result artifact.

`measure` emits one JSON number or a non-empty vector of at most 1,024 numbers.
The same command measures the baseline and result. A result measurement is also
collected after a failing predicate if time remains. Invalid numeric output or
changed dimensions prevent a successful verdict. The log records predicate
verdict, commit, evidence reference, measurement values, and their difference;
these bind the checkpoint and supervision folds described in [RECOVERY.md](RECOVERY.md).
For restored segments, measurement deltas use the restored input checkpoint;
their downloadable patches still describe the full change from the original baseline.

This local provider still runs everything as the operator. Directory separation
and hash checks do not make the oracle inaccessible to that user. Every predicate
record therefore says `oracle_isolation: same_user_process`, and the local provider
has no unattended grant. Local checkpoint boundaries, rewind, and restart intensity
are implemented. Enforced oracle isolation, provider snapshots, and dependency-aware
pipeline recovery remain required for the full system.
