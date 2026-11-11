# Bound artifact data dependencies

Status: Normative local behavior and API contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

A finish-to-start dependency can transport selected registered artifacts from
its producer. The Task remains the source of ordering and result selection; the
worker receives subject data only after the drainer records the selected result.

```json
{"from":"task_PRODUCER", "relation":"FS", "payload":"data",
 "strength":"hard", "guard":"done",
 "artifacts":["inventory.json", "report.md"]}
```

The shared brief/API scheduling schema admits this edge. CLI shorthand is:

```sh
./bin/mitos run --title 'Consume the inventory' --app consumer --workspace . \
  --input task_PRODUCER:inventory.json --input task_PRODUCER:report.md
```

Repeated selections from one task are grouped into a single data edge. `--input`
implies a successful producer (`guard: done`); the API supports the same terminal
status guards as control edges, including failed/cancelled results when explicitly
requested. New-work forms provide an artifact-selection field. The built-in
inventory can produce data, but its entrypoint does not consume external inputs;
submitting data inputs to that entrypoint is rejected.

## Admission and result identity

Each data edge selects 1–32 explicit artifact names. Names must be unique filenames,
not paths. Names containing separators, NUL, line breaks, or traversal components
are refused. A consumer is limited to 128 selected files and 64 MiB total input
bytes. These limits are checked from registered sizes before ownership acquisition,
and actual file sizes and bytes are checked again before execution.

Race and quorum rules select producer-result witnesses first. Only artifacts from
the selected hard-dependency witnesses are required. A losing branch cannot block
admission through a missing artifact. Matched soft data dependencies are considered
after required inputs; missing or over-budget optional data records an
`input_omitted` reason and does not block the task. Once optional bytes have been
promised, subsequent corruption is an error rather than silent omission.

Admission extends each dependency witness with the exact artifact name, SHA-256,
and byte count. The witness already identifies the producing Task, Run, terminal
status, and result-event sequence/hash. A missing required artifact fails the
consumer's queued receipt without claiming ownership or preparing execution.
A later producer retry cannot change an admitted consumer's selection. A new
consumer attempt rechecks upstream state; checkpoint recovery preserves the
existing run's bindings.

The transport requires a recorded typed Task result. A historical producer whose
only Task state is an inferred legacy projection must produce a new recorded result
before its bytes can be transported through this boundary.

## Materialization and worker context

Before each worker segment starts, Studio verifies the producer event witness and
artifact registration, opens each artifact through a confined directory handle,
rejects symlinks and non-regular files, and reads at most the recorded bounded size.
It verifies the complete digest before publishing the copied input tree. Archives
remain opaque bytes; no archive extraction or implicit patch application occurs.

Copies are staged and synced before directory publication. Each producer has its
own directory, preventing filename collisions. The files have mode `0444` and are
separate copies, not links into the producer's store. The worker's stdin envelope
contains:

```json
"inputs": [{
  "task_id": "task_PRODUCER", "run_id": "local_PRODUCING_RUN",
  "directory": "/consumer-segment/inputs/task_PRODUCER",
  "artifacts": [{"name":"inventory.json", "sha256":"…", "bytes":123}]
}]
```

Workers should use those supplied directory paths. `dependencies` also contains
the recorded result witnesses and optional omission reasons. Input copies live
outside the consumer workspace and are excluded from its patch/archive. A generated
`dependency-inputs.json` artifact records the selected names, hashes, sizes, and
consumer locations; later segments use the existing segment artifact prefix.
The fleet reader exposes the manifest reference and structured event headers,
without exposing artifact bodies or raw worker output.

Studio fingerprints all copied data, including Git-named files. After worker
shutdown and workspace harvest, it checks that fingerprint before acceptance.
It checks again after verification and before recording the predicate verdict.
Changed, added, removed, or replaced input data withholds acceptance and raises a
judgment card. The workspace patch and prior input tree remain available; there
is no automatic retry for an input-integrity violation. Explicit recovery creates
fresh copies in the next segment from the same saved producer binding.

## Limits of this implementation

The same-user local provider does not enforce an inaccessible input mount. File
modes and post-execution checks detect ordinary changes; a process with the same
UID can change permissions, mutate other host files, or restore bytes before an
inspection. These mechanisms do not grant an unattended horizon or isolation
claim. An isolated provider with protected read-only mounts remains required.

This implements explicit registered-artifact transfer for FS data edges. Typed
application outputs, data schemas, arbitrary result predicates, SS/FF/SF edges,
archive/dataset materialization, governed invoke/result continuations, and sibling
patch integration remain separate unimplemented mechanisms.
