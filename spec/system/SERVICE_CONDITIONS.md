# Conditional service execution

Status: Normative local behavior and API contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

Service units accept the retained `condition` object. All specified conditions
must pass before the application's command or builtin handler runs:

```json
"condition": {
  "file_exists": ".ralph/workers/worker-*",
  "file_not_exists": ".ralph/pause",
  "env_set": "GITHUB_TOKEN",
  "env_equals": {"WIGGUM_MODE": "production"},
  "env_not_equals": {"WIGGUM_RUN_MODE": "merge-only"},
  "command": "test -s input.json"
}
```

Omitting `condition`, or supplying an empty object, permits execution. Conditions
apply to manual, interval, cron, event, continuous, webhook, and external-signal Tasks, including
builtin inventory. Ordinary nonservice Tasks have no service condition gate.

## Evaluation

Environment checks run first, followed by file checks and the optional command.
The evaluator stops at the first mismatch or error. It records the kind/name and
pass result of each evaluated condition. It does not record actual environment
values, file contents, matched filenames, or full command output.

| Field | Meaning |
|---|---|
| `file_exists` | At least one source path matches the glob |
| `file_not_exists` | No source paths match the glob |
| `env_set` | The server environment variable is nonempty |
| `env_equals` | Every named variable equals its configured value |
| `env_not_equals` | Every named variable differs from its configured value |
| `command` | The shell command exits zero within the check deadline |

File checks inspect the selected original workspace, including ignored control
files. Paths are relative, with at most 1024 bytes and 32 components; absolute
paths and empty/dot/parent components are refused. `*`, `?`, and bracket classes
match within a component. A component must start with a literal dot to match a
hidden entry. There is no recursive `**` or shell expansion. Files and directories
both count as matches. Lookup follows links only within the opened source root;
lookup failures or escaping links withhold execution. Each glob visits at most
10,000 directory entries. A failed or incomplete search cannot satisfy a negative
file condition. The checks are point-in-time observations, not filesystem locks.

Environment values are sampled from the Studio server once per evaluation, not
from the CLI caller or worker environment. Unset variables compare as empty
strings; `env_set` rejects both unset and empty values. Names use the usual
letter/underscore followed by letters/digits/underscores syntax, up to 128 bytes.
Each comparison map has at most 32 entries with values of at most 1024 bytes.
Configured comparison values remain ordinary catalog data, so use `env_set` when
only credential presence matters. Actual environment values are neither persisted
in condition facts nor forwarded to the subprocess.

Command checks run `/bin/sh -c COMMAND` through the application's admitted provider
posture. The command has at most 4096 bytes. Studio prepares a separate bounded
copy of the source with private Git metadata, home, and temporary directories.
Ordinary source ignore rules apply to this copy. The working tree reflects source
file contents; its Git baseline is the prepared copy, not the source's dirty index
or branch. Use file conditions for ignored source control files.

Command changes stay in that condition workspace; they do not initialize the
application workspace or enter the application's result patch. The command uses
the provider's cleared environment, receives no worker stdin, and has no inherited
server credentials. Evidence retains its output SHA-256 and captured byte count
(up to the provider's 2 MiB limit). Its private workspace/provider records remain
under `conditions/RUN/CHECK` in the data directory for inspection. Probe output
is not exposed as a downloadable result artifact.

The whole check has a five-second deadline, further bounded by the attempt's
remaining execution time. This includes source preparation for command checks.
Timeout, lookup error, preparation failure, command launch failure, and nonzero
exit all withhold application execution. Cancellation stops the process group
through the provider and waits for cleanup before releasing the attempt's claims.

## Task and lifecycle behavior

Triggers still create durable Task/Run receipts. Queue admission acquires the
ordinary pool/service/ownership slot and binds the current condition policy and
catalog revision in the same Effect. The check then runs before application
workspace preparation, worker execution, or acceptance. A synced Decision precedes
evaluation and an Effect records its outcome.

A mismatch/error ends the Run and Task as `cancelled`, with stage **Condition not
met** and a `service_condition_skipped` Effect. This represents a skipped service
attempt; it is not a failed application. It requests neither acceptance nor
failure retry/abort and emits no service success/failure completion event. Schedule
submission counts still include its receipt. Condition outcomes are separate from
capacity-related schedule skips. Claims are released, and later independently
triggered work evaluates its own condition. A continuous controller waits its
configured restart delay before trying another Task.

A passing check permits ordinary execution; it is not acceptance evidence and
cannot override a failing verifier. Health monitoring, when configured, starts
later with the actual command. Condition preparation consumes the existing
attempt's slot and deadline and cannot extend a bounded service retry window.

Catalog edits before queue admission select the new policy. Once admitted, the
Run keeps its bound policy through later edits and recovery. Conditions do not
continuously monitor an already admitted invocation; use health checks for that.
An explicitly resumed interrupted Run evaluates its bound condition again with a
fresh check ID before resuming. Checkpoint supervision inside the same invocation
continues under the already-passed gate. New automatic or manual retry Runs bind
conditions at their own admission.

On server restart, uncertain execution parks under the normal recovery rules.
An unfinished condition check becomes `interrupted`; replay does not execute the
command or treat an unfinished check as permission to run. A recorded pass also
does not cause automatic execution replay. Manual trigger idempotency keys return
the original receipt, including a skipped one; use a new key for a fresh attempt.

## Inspection and limits

Run JSON includes `condition` with the bound policy, catalog revision, check ID,
status, timestamps, facts, and optional output digest/size. Status is `bound`,
`checking`, `passed`, `skipped`, `error`, or `interrupted`. The Services reader shows
the latest attempt's condition; older evidence remains on each Run and in its log.
The same state is visible with `mitos status RUN`, `mitos services`, their existing
HTTP APIs, and the Run/Services pages. Verified offline metric readers understand
these records without counting them as human decisions or accepted changes.

Conditions provide an execution gate, not readiness dependencies or isolation.
The current same-user provider does not confine a deliberately hostile command,
enforce read-only checks, or mediate egress. Conditions retain its zero unattended
grant. [Intra-cycle completion dependencies](SERVICE_DEPENDENCIES.md) gate admission
separately. Lifecycle propagation, custom event names and provider-specific ingress
connectors, generalized service readiness, and the broader product gates remain
outstanding.
