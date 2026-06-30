# Pipeline: meta-todo-hunter

Goal: find and address TODO/FIXME items in the codebase.

```yaml
budget: { usd: 1, wall_ms: 600000 }
allowed_capabilities: [todo-hunter]
success_criteria: [ "TODOs triaged; actionable ones addressed" ]
```

## Stages

1. **todo-hunter** — grep TODO/FIXME, triage, address the actionable ones.

## Notes

- Single-step pipeline. Run on a schedule via a service unit (`interval`) to keep TODO debt
  bounded over time; the pipeline itself is one pass.
