# Service health checks

Active services bound to command applications can monitor the running worker with
an optional `health` declaration on the service unit:

```json
"health": {
  "type": "file",
  "path": ".ralph/services/heartbeat",
  "max_age": 60,
  "interval": 30,
  "on_unhealthy": "restart"
}
```

Omitting `health` disables monitoring. Within a declaration, the defaults are
`type: heartbeat`, `max_age: 60`, `interval: 30`, and `on_unhealthy: log`.
Durations are whole seconds, bounded to 1–31536000; omitted or zero values use
the defaults. Actions are `log`, `kill`, and `restart`.

## Probe types

| Type | Configuration | Healthy evidence |
|---|---|---|
| `file` | `path`, `max_age` | A regular file in the live worker workspace with a modification time no older than `max_age` |
| `command` | `command` | The configured shell command finishes successfully within its probe deadline |
| `heartbeat` | `max_age` | The current monitor has a recent server-recorded heartbeat, or remains within its initial grace period |

File paths must be relative, without parent traversal, and at most 1024 bytes.
Root-confined filesystem lookup rejects symlinks that escape the worker workspace.
Missing files, nonregular files, stale timestamps, and timestamps in the future
are unhealthy. The probe reads metadata, not the file body. A heartbeat file in
the original source directory does not refresh its copied worker counterpart.

Command probes run `/bin/sh -c COMMAND`, with a nonblank command of at most 4096
bytes. They use the worker's bound provider and launch posture, its live working
directory, and separate `HOME`/`TMPDIR` directories. The local provider uses its
usual cleared environment and explicit variables. The deadline is the shorter of
five seconds and `interval`, further bounded by the worker execution context.
Failure to start, a nonzero exit, and timeout are unhealthy. Each monitor runs at
most one probe at a time. Checks wait `interval` after the previous result; they
do not overlap or catch up missed checks.

The log retains the SHA-256 digest and captured byte count of command output
(the provider capture limit is 2 MiB), plus bounded failure detail. Full probe
output is not a downloadable result artifact. Provider launch records and probe
home/tmp directories remain in the data directory. Probe processes are management
work within the admitted attempt, not additional Tasks or pool reservations.

## Heartbeat protocol

Each command execution segment gets a fresh random monitor ID. The first check
is due after `interval`; before the first heartbeat, freshness is measured from
monitor startup. Worker stdin includes:

```json
"service_health": {
  "monitor_id": "health_...",
  "segment_id": "segment_...",
  "heartbeat_route": "/api/runs/local_.../heartbeat"
}
```

Send heartbeats to the running Studio using the supplied route:

```http
POST /api/runs/local_.../heartbeat
Content-Type: application/json

{"monitor_id":"health_...","sequence":1}
```

The equivalent CLI call is:

```sh
mitos heartbeat --url http://127.0.0.1:4646 \
  --monitor health_... --sequence 1 local_...
```

The server URL is supplied by the application/operator; stdin supplies a relative
route. Sequences are positive unsigned 64-bit integers and must increase within
one monitor. The server supplies the timestamp. Repeating the current sequence
returns the recorded state without refreshing its timestamp or appending an event.
Lower sequences, old monitor IDs, closed scopes, and nonrunning attempts return
409; malformed requests return 400. A new segment starts its sequence at zero.
A heartbeat arriving during evaluation or before the stop decision supersedes an
obsolete expiry result while monitoring continues. Once a stop decision has been
recorded, a later heartbeat cannot retract that action.

## Actions and retry bounds

`log` records unhealthy evidence and leaves execution running. Subsequent checks
can report recovery. `kill` stops the current attempt, preserves its available
output and workspace changes, and fails it without an ordinary failure retry.
`restart` uses the existing [service retry policy](SERVICE_RESTARTS.md): the next
attempt belongs to the same Task, consumes its retry count, waits its backoff,
and retains the original absolute deadline. Health restart applies even when
ordinary `on_failure` is `skip`. It does not bypass admission, claims, dependencies,
service capacity, or circuit breakers.

The retry defaults remain two additional attempts and backoff of five seconds,
multiplier two, capped at 300 seconds. Explicit zero retries disables automatic
health retries. Explicit `on_failure: abort` takes precedence over either stop
action and latches the service abort. Health failure bypasses checkpoint restart
supervision so that a separate recovery loop cannot evade the service retry bound.
Ordinary command/acceptance failures still follow ordinary failure policy.

`kill` ends the current attempt; a later scheduled occurrence or a continuous
controller's next desired generation can still run. Disable the service to
withdraw future desired work. Both health stop actions use the provider's normal
stop primitive; the current local provider kills the process group. No graceful
termination signal or application shutdown hook is promised.

## Binding, persistence, and recovery

Admission saves the effective health policy and catalog revision. Each command
segment starts its own scope; preparation/verification outside that command is not
periodically health-checked. A synced Decision precedes each probe, an Effect saves
its result, and another synced Decision precedes a requested stop. Probe shutdown
is joined before workspace harvest. Health state is visible in Run and Services
pages, their JSON APIs, and CLI readers; prior checks remain in event history.

The result and action paths recheck the current attempt, monitor, activation, and
effective health policy. Policy withdrawal or change closes the old monitor;
an in-flight result cannot stop work under the changed policy. New policy binds
at a new admission, rather than retroactively rewriting the current Run. Changes
to activation, health policy, or restart policy suppress pending health retries.
Unrelated catalog edits preserve monitoring. Operator cancellation uses its
existing stop path and takes precedence over automated health failure handling.

Server restart parks interrupted execution and closes old monitor scopes. Pending
checks become interrupted evidence; replay does not rerun a probe or repeat an
uncertain stop action. Existing recorded retry backoff follows normal restart
recovery. Crash-held claims still require the existing inspection procedure.

## Scope

Health reports liveness, not readiness, acceptance, or permission for unattended
execution. Active builtin inventory applications with health configured are
explicitly refused; generalized builtin monitoring and dependency readiness gates
remain unimplemented. Lifecycle propagation, custom event names and provider-specific ingress connectors, distributed
health/fencing, and the full product gates also remain outstanding.

Command probes can modify the live workspace; those changes enter ordinary harvest
and acceptance checks. The local same-user provider does not enforce read-only
probes, filesystem/credential isolation, resource limits, or mediated egress.
Heartbeat and file freshness are self-reported activity, and successful checks
cannot override acceptance failure or raise the zero unattended grant.
