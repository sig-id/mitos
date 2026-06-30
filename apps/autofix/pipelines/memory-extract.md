# Pipeline: memory-extract

Goal: analyze a completed worker/run and extract reusable patterns into memory.

```yaml
budget: { usd: 1, wall_ms: 600000 }
constraints: [ "read-only analysis" ]
allowed_capabilities: [memory-analyst]
success_criteria: [ "patterns distilled and written to memory" ]
```

## Stages

1. **memory-analyst** — read the run trace, distill reusable patterns into memory. (read-only)

## Notes

- Single-step, read-only. Pairs with the memory plane (`spec/07`): its output seeds retrieval
  slices for future runs. Typically triggered as an `on: run.done` follow-up Task rather than run
  manually.
