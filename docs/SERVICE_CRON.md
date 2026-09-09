# Service cron schedules

A service unit can use a five-field calendar schedule:

```json
{
  "id": "inventory",
  "scope": "project",
  "trigger": "cron",
  "schedule": {
    "type": "cron",
    "cron": "*/15 9-17 * * 1-5",
    "timezone": "Europe/Zurich",
    "run_on_startup": false
  }
}
```

The work binding, desired activation, pool/dependencies, concurrency, circuit
breaker, and restart policy use the existing service catalog. Cron occurrences
create ordinary Tasks. `timezone` defaults to `UTC`; `run_on_startup` defaults to
false. Interval and jitter values cannot be combined with a cron schedule.

## Expression semantics

Fields are `minute hour day month weekday`:

| Field | Values |
|---|---|
| Minute | 0–59 |
| Hour | 0–23 |
| Day of month | 1–31 |
| Month | 1–12 |
| Weekday | 0–6, Sunday through Saturday |

Each field supports `*`, numeric values, ascending ranges, comma-separated lists,
and positive integer steps. Examples include `1,3,5`, `1-10/2`, `*/15`, and `5/15`.
Names, seconds/year fields, macros, negative values, reversed ranges, empty list
items, zero steps, and weekday 7 are refused. Expressions are bounded to 256 bytes.

The retained service matcher requires **all five fields to match**. Day-of-month
and weekday are a conjunction: `0 9 13 * 5` selects Friday the 13th at 09:00.
It does not select every Friday or every 13th. Wildcard steps retain value-modulo
semantics: `*/2` in the day/month field selects even numbered days/months. Steps
on a numeric start or range are relative to that start.

Date validation rejects calendars such as February 30. Next-occurrence lookup
covers a full 400-year Gregorian cycle, so rare combinations such as leap day on
a Monday are supported without a short lookahead cutoff. An active schedule
with no actual matching instant in that cycle is refused during preview/save.

## Timezones and clock changes

Cron uses IANA timezone names, with bundled Go timezone data available as fallback.
Machine-dependent `Local`, arbitrary paths, and unknown zones are refused. Results
are recorded as UTC instants even when the expression uses local time.

Nonexistent local times during a forward clock change are skipped. Repeated local
times during a backward change produce both matching instants, in UTC order, with
distinct occurrence keys. This includes half-hour transitions. Non-hour timezone
offsets are supported. A next occurrence is always strictly after the evaluation
instant; evaluating again at a committed due time cannot duplicate its Task.

Newly calculated initial clocks are persisted with catalog configuration. Replay
uses the saved instant, not a recalculation from a potentially newer timezone
database. Unchanged active schedules retain their saved clock across unrelated
catalog edits. Future calculations use the runtime's timezone rules. If those
rules leave a previously saved schedule with no future match, the scheduler
records a rejection reason and pauses that clock without stopping other dispatch.
A configuration change can revalidate/resume it; manual triggering still uses
ordinary admission gates.

## Startup, downtime, and evidence

With `run_on_startup: true`, activation is immediately due and engine startup
requests an immediate occurrence. Subsequent occurrences return to matching
calendar boundaries. Startup requests obey service capacity, breaker, and abort
rules. With false, restart retains the saved future due time.

After downtime, an overdue clock produces at most one occurrence, retaining the
saved due time and actual observation time. The next cursor is the first calendar
match after observation. Missed occurrences are coalesced; there is no backlog
replay or interval-style drift from the observation time.

Task/Run creation, cron provenance, occurrence identity, and next cursor are synced
in the same Effect. Capacity/breaker/abort skips and intake rejections advance the
clock with evidence. Replay reconstructs that exact state. Disabling desired
activation stops future clock submissions while existing Tasks continue under
their execution gates.

`services-preview` and `/api/services/preview` include proposed `clock_preview`
instants without mutation. The normal Services view shows saved next/last times,
outcomes, and runtime state. The catalog writer supplies clocks; they are not
client-authored catalog fields. Event history retains their authoritative values,
and live/offline metrics treat cron Tasks as ordinary runs.

This is local calendar scheduling, checked by the existing 200 ms dispatcher tick;
it is not a real-time deadline guarantee. Cron does not add a positive unattended
grant or hosted tenant isolation. Webhook/external triggers,
readiness gates, and other remaining product mechanisms are still tracked in the
implementation status. See [services](SERVICES.md) for configuration and limits.
