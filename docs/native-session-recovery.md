# Native session model health and recovery

Interactive native sessions run a coding CLI in a managed terminal. The CLI's
own model calls can fail transiently (HTTP 408, 425, 429, 5xx) or terminally
(4xx such as invalid credentials). Mitos cannot retry those calls from the
outside, so recovery executes inside the session through the injected harness
adapter, under an operator-owned policy, and every step is reported back and
stored on the session as evidence.

## Retry policy

The project profile carries an optional `retry_policy` object; omitted fields
use the built-in default. The policy is delivered to the session through the
`MITOS_RETRY_POLICY` environment variable and executed by the adapter (the Pi
attention extension today).

```json
{
  "transient_codes": [408, 425, 429, 500, 502, 503, 504],
  "max_attempts": 3,
  "backoff_seconds": 5,
  "backoff_multiplier": 2,
  "max_backoff_seconds": 60,
  "retry_after_cap_seconds": 600,
  "fallback_models": []
}
```

Semantics:

- Transient codes are always retried while attempts remain. One burst of
  `max_attempts` covers the original call plus retries on the same model.
- Backoff grows by `backoff_multiplier` per retry and is capped at
  `max_backoff_seconds`. A provider `Retry-After` hint (seconds) replaces the
  computed delay and is clamped to `retry_after_cap_seconds`, never extended.
- When a burst is exhausted, `fallback_models` are tried in order; each
  fallback model gets a fresh burst. A fallback that is not installed or has
  no credentials is skipped and reported.
- Errors without an HTTP code, and non-transient codes, are surfaced to the
  operator without automatic retry. Operator aborts (Escape/interrupt) are
  never retried.
- A scheduled retry is dropped if the operator takes over the terminal first.

Retries steer a visible continuation message into the session; recovery never
runs hidden. The policy is guidance executed in the agent process, not an
OS-level guarantee.

## Status reports

Adapters report through `POST /api/sessions/{session}/agent-status` with a
bounded body: `state` (`healthy`, `retry_scheduled`, `model_switched`,
`retry_exhausted`, `error`), the failing message and HTTP code, attempt
counters, the scheduled delay, and cumulative per-model token usage. Reports are
telemetry from inside the agent process, never session status or completion
claims. They are accepted for stopped sessions so shutdown races do not
discard final evidence, and they persist across restarts with the session.

The workspace terminal view shows the live position beside the connection
status: scheduled retries with their model and time, model switches after an
exhausted budget, terminal model errors, and cumulative token usage. The
session JSON exposes the same evidence as `agent_error`, `retry_state`, and
`model_usage`, a per-model map so consumption stays attributed to the model
that produced it across fallback or operator switches. Estimated dollar costs
remain scoped to coordinated runs; native-session usage is reported per
session as token counts.

## Operator commands and fan-out

An initial prompt that begins with `/` is a native CLI command (for example a
Pi `/goal` fleet brief) and is passed to the CLI verbatim, without the Mitos
session-context wrapper that would break command parsing. Coordination
context remains available through `MITOS_COORDINATION` and the desk CLI.

Session context distinguishes lineage: a worker adopted by an orchestrator is
told to request additional agents through the orchestrator instead of native
CLI sub-agents, while a session the operator started directly is told that
operator-authorized native fan-out is acceptable for that assignment, keeping
durable, reviewable work in Mitos sessions.
