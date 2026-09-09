# Local checkpoints and bounded recovery

Installed command applications now record an initial checkpoint and a checkpoint
after each stopped, successfully harvested segment. A checkpoint binds a canonical
log offset, a recorded 200 ms quiet boundary with no running command tree, an exact
Git commit and content fingerprint, and its acceptance evidence. Only a passing
predicate makes it green. Missing verification records an unknown verdict.

The built-in inventory application does not use this command lifecycle. The local
provider records `sandbox_snapshot: unsupported` and predicate isolation as
`same_user_process`. These checkpoints do not establish an inaccessible oracle or
grant unattended operation.

## Configuration

Add this optional field to a local application's `app.json`:

```json
{
  "supervision": {
    "strategy": "rewind_to_green",
    "max_restarts": 3,
    "max_seconds": 3600
  }
}
```

Configured supervision checks the initial workspace with the saved acceptance
command before starting segment 1. An initial failure permits work to start but
does not provide a green recovery point. Applications without supervision retain
ordinary one-command failure behavior and an unknown initial checkpoint.

| Strategy | Local behavior after a command or predicate failure |
|---|---|
| `retry_step` | Restore the last green checkpoint of the current step |
| `rewind_to_green` | Restore the last green checkpoint |
| `rewind_tail` | Restore green and invalidate the subsequent local segments |
| `escalate` | Park for an operator decision |

The current application dialect has one step, `main`. Step and tail scopes
therefore cover that same step. Dependency-aware recovery across a composed
pipeline remains outstanding; these strategies are not an orchestration agent.

The default intensity limit is three rewind requests in 3,600 seconds, enforced
across all reason signals for both the run and step. Explicit `max_restarts: 0`
disables rewind. Limits allow 0–100 restarts and windows up to 86,400 seconds;
omitted or zero `max_seconds` selects the default. Durable requests count even if
restoration crashes, and a backward clock jump does not reset the budget.

Three consecutive completed segment predicates with zero numeric deltas trigger
`predicate_stall`, irrespective of file churn. When no numeric measure is declared,
three identical known binary verdicts provide the plateau signal. Missing or
incompatible measurement evidence does not count as zero. Abandoned segments
remain part of this history. The first stall can cause one bounded rewind; a
repeated stall parks. A missing green checkpoint, exhausted intensity, or an
execution infrastructure error also parks. Oracle tampering, lost observation,
human blocking, cancellation, and deadline expiry do not trigger automatic retry.

## Restoration and crash behavior

A rewind requires a failure memo and reason. Studio fsyncs its Decision before
restoring exact checkpoint bytes into a new segment workspace. Restoration checks
the commit's fingerprint, creates independent Git metadata, syncs files and
directories, then publishes the directory atomically. An existing destination is
accepted only if both its contents and Git HEAD still match the checkpoint.
The subsequent restore Effect marks later local segments/checkpoints superseded;
their workspaces, outputs, and records remain available.

A restart resumes a pending restoration, including a crash after filesystem
publication but before the Effect. It rechecks a completed restoration before
launching the next segment. An interrupted open command still parks because its
possible external effects cannot safely be inferred from workspace state.

The persisted original execution deadline applies to every segment and includes
time spent waiting for an operator. Rewind never refreshes it. A new attempt is a
separate run with its own budget and retains the same task identity.

Each command receives the unchanged `brief` plus a `segment` object and
`failure_memo` on stdin. The first segment has an empty memo. Automatic recovery
names the preceding segment's failure, output, and change manifest. The phone
decision supplies a deterministic memo; CLI/API callers supply their own.

## Evidence and operator actions

The first segment retains the ordinary artifact names. Later segments use
`segment-002-output.txt`, `segment-002-changes.patch`, `segment-002-changes.json`,
`segment-002-workspace.tar.gz`, and similarly prefixed predicate/measurement files.
The initial predicate uses `initial-acceptance.txt`. Each segment's patch describes
the full change from the original run baseline; numeric deltas compare its actual
restored input with its result. Earlier artifacts are never overwritten by rewind.

Run pages show recovery points. Parked decision cards offer restoration when a
green checkpoint, remaining intensity, a finished segment, and time remain.
Selecting it uses the same durable operation as the CLI/API. Repeated submissions
of the same phone decision resolve once.

```sh
mitos status local_RUN_ID
mitos checkpoints local_RUN_ID
mitos rewind --from-segment 1 --memo 'Repair the failure recorded in output.txt.' local_RUN_ID
```

`--from-segment` is required and must equal the current number of started segments.
Use `--checkpoint CHECKPOINT_ID` to select an available checkpoint, `--scope tail`
for local tail recovery, and `--reason SIGNAL` to replace `operator_recovery`.
A selected non-green checkpoint additionally requires `--allow-non-green`.

`GET /api/runs/{id}/checkpoints` returns the folded checkpoint records.
`POST /api/runs/{id}/rewind` accepts exactly one JSON object:

```json
{
  "checkpoint_id": "checkpoint_...",
  "scope": "step",
  "memo": "Repair the failure recorded in output.txt.",
  "reason_signal": "operator_recovery",
  "from_segment": 1,
  "allow_non_green": false
}
```

Omit `checkpoint_id` to select the last available green point. Successful requests
return HTTP 202 with the durable rewind record. Malformed requests return 400;
stale state, missing memo, disallowed targets, terminal runs, expired deadlines,
and exhausted restart budgets return 409. Identical requests while restoration
is pending or ready return the existing record. Once another segment starts,
the old `from_segment` cannot launch duplicate work.

## Remaining autonomy work

Enforced oracle isolation, provider snapshots, native harness contracts,
dependency-aware pipeline recovery, model-driven policy, fleet supervision,
admitted unattended execution, and evaluation remain required by the full specification.
The implemented [autonomy metrics](AUTONOMY_METRICS.md) measure recorded recovery
without granting permission from those measurements.
The Studio overnight and Market gates remain unmet.
