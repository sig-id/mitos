# Pipeline: intent-distill

Goal: distill codebase patterns into Intent DSL specifications.

```yaml
budget: { usd: 2, wall_ms: 900000 }
constraints: [ "read-only distillation" ]
allowed_capabilities: [intent-distill]
success_criteria: [ "intent specs emitted for the scanned patterns" ]
```

## Stages

1. **intent-distill** — read code, distill patterns into Intent DSL specs. (read-only)

## Notes

- Single-step, read-only. Output specs live in `intent/` and become authoritative for future
  spec-drift audits (the random-audit `spec drift` concern, §61).
