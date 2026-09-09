# Local fleet coordination and evidence

Studio acquires declared path ownership and singleton-resource claims durably
before dispatch. Command applications default to owning the whole source workspace
(`.`). The read-only inventory application takes no implicit write claim.

A brief can declare literal relative file or directory prefixes and exact resource
names:

```json
"ownership": {
  "paths": ["src/component", "tests/component"],
  "leases": ["database:integration"]
}
```

Paths are prefixes with directory boundaries, not globs: `src` overlaps `src/a`,
but does not overlap `src2`. Roots are canonicalized at submission. Overlap checks
also account for nested source roots. Resource names conflict across workspaces
in the same Studio store. Limits are 128 unique path prefixes and 32 resource
names per brief. Separate Git worktrees are not recognized as one logical repository.

Conflicting work remains queued and records `dispatch_conflict` when the conflict
set changes. It consumes no execution slot. Disjoint work can run concurrently
within the existing two-execution limit. Claims remain held through command exit,
workspace harvest, verification, and any automatic recovery. Completion records
release in the terminal event; controlled failure, cancellation, or parking releases
after the managed execution stops. Harvested changes outside declared paths retain
their patch and archive but withhold acceptance and raise an ownership judgment card.

After a server crash, claims with no recorded release stay held, including when an
automatic rewind left the run queued. The run requires inspection. Neither elapsed
time nor cancelling an orphan's record proves its processes stopped. The operator
must inspect and stop the previous processes, then explicitly release the claims:

```sh
./bin/mitos release-claims --confirmed-stopped local_RUN_ID
```

The run page provides the same confirmation operation. The API accepts one strict
JSON object, `{"confirmed_stopped":true}`, at
`POST /api/runs/{id}/claims/release`. Active managed executions cannot use this
operation. The durable confirmation records the local operator and release reason;
a duplicate does not change the record. A saved rewind can then resume with its
original checkpoint, memo, restart count, and deadline. A new attempt inherits
the same ownership declarations.

These claims coordinate declared work within one Studio store. They are not
filesystem confinement, kernel resource locks, external fencing tokens, or proof
that escaped same-user processes have stopped. They do not merge sibling patches
or implement a dependency-aware integration task.

## Structured evidence reader

`mitos fleet` / `GET /api/fleet` returns the newest 128 run summaries.
`mitos evidence RUN_ID` / `GET /api/runs/{id}/evidence` returns one coherent
structured view from the store's replayed event prefix. The run page links to it.
The view includes status and capability reference, claim state, workspace commits,
changed-file count, checkpoint and segment records, numeric predicates, selected
control-event headers, and canonical diff/verifier artifact references with hashes.

The dedicated DTO has no brief text, summary, decision-card text, failure memo,
rationale, arbitrary event payload, worker output, raw session record, or artifact
body. Enum fields and control kinds are allowlisted; unknown values do not become
free-form text. Artifact names are restricted to generated change and verifier
names. A reference to a verifier output is metadata, not that output's contents.
Ordinary operator artifact downloads remain separately available and verify hashes.

Blocker references are capped at 128 with a separate total/omitted window.
Each history collection returns at most its most recent 128 entries and reports
its total and omitted count. The current workspace and predicate remain separate
from the history windows. Measurement vectors retain the acceptance parser's bound
of 1,024 numbers. Omitted history must not be treated as absent history. The fleet
index is a bounded recent view; an older known run remains readable by ID.

The evidence includes its run's last event sequence/hash and a SHA-256 digest of
the complete JSON DTO with `digest` set to the empty string. A later event changes
the anchor even when that event's text is excluded. Reopening the same durable
prefix yields the same digest. The digest identifies evidence; it grants no action
authority. The legacy event schema does not supply authenticated actor provenance,
so this reader does not invent it.

This is the input seam for future fleet supervision. There is no admitted supervisor
capability, metering, supervisor identity, or cross-run mutation endpoint here.
The current same-user provider cannot stop a process from reading the raw operator
log or other local files. An isolated provider, scoped identity, restricted Decision
admission, orchestration, and integration tasks are still required by `16 §5`.
