# Local observation and interruption evidence

Status: Normative local behavior and API contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

The first Go observation adapter watches a trusted local command without
changing its argv or mode. It supplies a shared quiescence clock and can tail
an explicitly declared structured session artifact. It does not yet integrate
an upstream interactive harness or grant unattended execution.

## Enable an installed command

Add an `observation` object to its `app.json`:

```json
{
  "observation": {
    "think_time_p95_ms": 60000,
    "session_artifacts": {
      "path_template": "session.jsonl",
      "format": "mitos-jsonl",
      "schema_version": "1",
      "tail": true
    }
  }
}
```

Replace `60000` with the measured p95 for this command/harness; the example is
not a calibration result. Values must be between 1 and 86,400,000 ms. The local
policy sets the blocking threshold to `max(45 seconds, 2 × measured p95)`;
snapshot and steer thresholds are 200 and 750 ms. These are clock edges only:
this adapter has no PTY snapshot or steer implementation.

`session_artifacts` is optional. When present, `path_template` is currently a
literal relative path under the command's isolated `HOME`, not a glob or a
variable expansion. The command receives its normal JSON stdin and environment.
It may append these normalized records to `$HOME/session.jsonl`:

```jsonl
{"type":"status","status":"working"}
{"type":"assistant_text","text":"Preparing the change."}
{"type":"tool_use","tool_id":"t1","tool_name":"test","input":{"suite":"unit"}}
{"type":"tool_result","tool_id":"t1","output":{"exit_code":0}}
{"type":"token_usage","input_tokens":120,"output_tokens":30}
{"type":"final_result","text":"Ready for independent verification."}
```

The tail accepts complete newline-terminated JSON objects. It records source
path, start/end byte offsets, row SHA-256, and normalized events in the canonical
log. Partial rows wait for completion. A failed durable append does not advance
the consumed offset. Replacement, truncation, prefix edits, unknown fields or
types, negative usage, missing required fields, a missing artifact at exit, and
an incomplete final row fail the observation contract. Limits are 1 MiB per row
and 16 MiB per session file. Path traversal outside the isolated home is refused.

`Normalizer` is the native-schema adapter seam. The shipped normalizer accepts
only `mitos-jsonl` version 1. No Claude, Codex, or other upstream session schema
is claimed to be supported. Harness session records are evidence of harness
claims; they never directly change run state, grant authority, or mark acceptance
as passed. The separate acceptance command still determines the local verdict.

## Activity and stalled commands

Linux inotify watches workspace mutations, including writes of identical content
and new subdirectories. A sampled procfs process tree contributes hashes of argv;
raw command arguments are not recorded by this observer. Repeated argv in the
rolling blocking window do not reset the clock. Stdout/stderr byte activity also
resets the local command clock, even after the output artifact reaches its cap.
This is conservative display activity, not a source of progress semantics.

Each quiet period emits at most one `quiescent` Effect per threshold. If a known
live process remains quiet at the blocking threshold, the engine records
`blocked_on_human` and an escalation Decision, stops the local process group,
saves output, and parks the run on an external decision card. Its modal state is
unknown; the operator can restart in a new attempt or close the work. There is
no automatic restart or default. The worker slot is released. Observation
contract failure similarly records `observation_failed` and parks before
acceptance. Parked decisions and evidence survive log replay.

Procfs sampling can miss short-lived execs and escaped descendants; inotify is
not a mediated filesystem boundary. Overflow and detected observation loss stop
execution, but these facilities are not a substitute for the external engine's
complete effect surface. `observation_configured` records `exec_coverage:
"sampled_procfs"` and `unattended_eligible: false`. Apps without observation keep
their existing bounded command lifecycle. Neither configuration satisfies the
Studio overnight gate.

Still required: native upstream adapters and nightly contract suites, persistent
adapter quarantine, full observation admission policy, mediated process-tree
coverage, PTY spool/snapshot/classification/verified steer, and sandbox
rehydration after a decision. These remain part of the full-system objective.

## Measure interruptions

```sh
mitos interrupts                         # GET /api/interrupts from Studio
mitos interrupts --logs .mitos            # audit current JSONL without a writer
mitos interrupts --logs .runs             # inventory historical records
```

The live and offline surfaces use the same fold. It counts explicit decision
requests and first human resolutions, separates outstanding cards from actual
human touches, excludes labelled demo runs, and reports fractions only when a
human-touch denominator exists. Classes are recorded metadata:
`approve_the_boring`, `missing_information`, `judgment`, `rescue`, or
`unclassified`. Free text and terminal output are never used to guess a class.
The current recovery card records `rescue`; an unknown stall or lost observation
records `judgment`.

Offline reading validates current Go hash chains and rejects damaged complete
records and incomplete tails without modifying the file. Historical BEAM records
are counted separately; their decision schema and timestamp semantics are not
assumed compatible. This prevents old fixture timestamps from fabricating years
of real session coverage. The report does not certify a representative corpus
or calibrate a harness from counts alone.

The repository baseline collected on 2026-09-09 is stored in
[interrupt-baseline.json](evidence/interrupt-baseline.json): 11 historical runs, 133
records, and no supported human resolutions. The corresponding historical
`approvals.jsonl` files contain zero records. This corpus cannot supply the
requested two-week interruption distribution or observed harness think times;
collecting those remains outstanding.
