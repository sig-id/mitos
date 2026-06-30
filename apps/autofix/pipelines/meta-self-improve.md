# Pipeline: meta-self-improve

Goal: analyze recent run performance and improve project guidance/memory so future runs do
better.

```yaml
budget: { usd: 1, wall_ms: 600000 }
allowed_capabilities: [self-improver]
success_criteria: [ "guidance/memory updated with concrete, sourced lessons" ]
```

## Stages

1. **self-improver** — review recent runs, distill lessons, update guidance docs / memory.

## Notes

- Single-step pipeline. The memory it writes feeds the memory plane (`spec/07`) and the per-model
  skill-adapt seam, so improvements compound across runs.
