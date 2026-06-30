# Pipeline: failure-recovery

Goal: recover a failed task — summarize the failure for triage/GitHub, then attempt a repair.

```yaml
budget: { usd: 2, wall_ms: 900000 }
constraints: [ "do not mask the original failure", "post an honest summary if unresolvable" ]
allowed_capabilities: [failure-summarizer, failure-resolver]
success_criteria: [ "failure summarized; resolved, or documented as unresolvable" ]
```

## Stages

1. **failure-summarizer** — read the failure trace, summarize root cause. (read-only) On FAIL,
   `abort`.
2. **failure-resolver** — attempt recovery. On FIX, `retry` self.

## Notes

- In the original this was driven by `on_result: { FIX: jump:self }`. Here the loop-manager
  bounds self-retries via the `repeated_error`/`oscillation` signals and budget; the
  orchestrator `retry`s with feedback until the ladder is exhausted, then `escalate`s.
