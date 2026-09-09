# Autonomy evidence and execution grants

`mitos autonomy`, `GET /api/autonomy`, and Studio's `/autonomy` page share one
reader over recorded events. The Overview links to that page. The report separates
measured outcomes, incomplete evidence, and permission to run unattended.

```sh
mitos autonomy
mitos autonomy --logs .mitos
mitos autonomy --logs .runs > autonomy-baseline.json
```

Offline auditing opens no writer, repairs nothing, and executes no recorded
command. It verifies current JSONL chains and rejects damaged or partial records.
Identical run copies are counted once; divergent copies of a run are refused.
Historical formats without the required semantics are counted as unsupported.
Example runs are excluded using their creation event, not a mutable display flag.

## Definitions and missing evidence

| Report value | Basis |
|---|---|
| Accepted changes | Completed verified runs with a nonempty harvested change and a retained green checkpoint matching the final result commit |
| Productive wall time | Union of completed, retained green segment intervals belonging to accepted changes; overlapping intervals across runs count once |
| Autonomy ratio | Productive wall milliseconds divided by human decision requests raised, including unresolved requests |
| Required decision horizon | Time from the recorded run start to its first card without a default; queued time is excluded |
| Decision precision | Non-default choices divided by cards raised; a point value is withheld while any cards are unresolved |
| Rewind depth | All discarded segments, including those in unsuccessful runs, divided by accepted changes |
| Green rate | Passing checkpoints divided by all checkpoints taken, including initial and subsequently superseded points |

Productive wall time is a conservative verifier-based proxy. It does not measure
business value, and does not count a no-change run or the built-in inventory as an
accepted code change. Per-run productive times may sum to more than the fleet's
union because workers can overlap.

A green checkpoint must bind its recorded event offset, run, quiet boundary,
predicate commit, and previously registered evidence reference. A final result must
match that checkpoint. Narration, terminal bytes, a harness's claimed success, and
mutable run projections cannot manufacture acceptance, green points, or a grant.
The reader does not independently rerun the predicate or re-download its artifact;
those remain separate verification operations.

A zero denominator produces JSON `null`, displayed as **Not measured**. It never
produces an infinite autonomy ratio or a perfect rate. Unanswered cards produce
precision bounds: known non-default choices divided by all raised cards is the
lower bound; adding unresolved cards gives the upper bound. Default expiry counts
as a resolution but never as a human touch. The interruption report now preserves
that distinction too. No default executor is introduced by this reader.

## Censored horizons and clock evidence

A run with no required decision has not demonstrated an infinite horizon. Its
record supplies a right-censored interval: the first required decision is later
than the last observed point. For completed runs, observation ends at completion;
later audit or duplicate-resolution records do not extend it. For unfinished runs,
the bound uses the last recorded timestamp, not the reader's current wall clock.

The report computes median bounds over exact decision times and these censored
intervals. It publishes `unattended_horizon_p50_ms` only when the lower and upper
bounds coincide. `observed_decision_horizon_p50_ms` separately gives the median of
observed decision times and must not be mistaken for the entire cohort's horizon.
No independent-censoring assumption or fitted survival model is introduced.

Missing or backward timestamps exclude affected productive intervals. Known
started runs with unusable timing contribute unknown horizon intervals rather
than falsely precise durations. The report counts this loss of coverage and
withholds the autonomy ratio when timing evidence is incomplete.

## A measured horizon is not a grant

New command execution plans record a `HorizonGrant`; run projections also expose
its `unattended_until`. Admission requires all of:

- A structured observation source.
- An established observation contract.
- A machine-checkable predicate.
- Enforced oracle isolation.
- Enforced workspace confinement.

A granted duration is bounded by the original execution deadline. Missing facts
produce zero time and explicit reason codes. These inputs come from the substrate,
not a model's claim or the metric report.

The current local implementation records zero: sampled observation and a
same-user verifier do not establish the necessary contract and isolation. Old runs
without a grant remain unproven; the reader does not retroactively grant them a
horizon. There is no new unattended launch mode or relaxation of execution policy.
Positive grants still require admitted provider/harness/verifier integrations.

## Corpus evidence and remaining evaluation work

`autonomy-baseline.json` is a read-only audit of the available `.runs` corpus.
It contains 11 unsupported historical runs and no measured autonomy rates. It
cannot establish the Studio overnight gate or representative two-week coverage.

Tests exercise the reader on a real checkpoint/recovery lifecycle and compare the
live fold with the verified offline audit. Separate cases cover censoring bounds,
unanswered and defaulted decisions, invalid clocks, post-completion audit records,
concurrent intervals, failed-run discarded work, narration, and damaged chains.
A live CLI/API/restart smoke check verifies that all surfaces return the same report.

This supplies measurement and deterministic metric replay. It does not yet supply
representative real-session labels, policy/classifier/model replay evaluations,
fleet supervision, enforced positive unattended grants, or either product gate.
