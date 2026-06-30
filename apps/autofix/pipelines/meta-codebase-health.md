# Pipeline: meta-codebase-health

Goal: scan the codebase for quality issues and refactoring opportunities; address the top ones.

```yaml
budget: { usd: 1, wall_ms: 600000 }
allowed_capabilities: [codebase-health]
success_criteria: [ "scan complete; top issues addressed or documented" ]
```

## Stages

1. **codebase-health** — scan for hotspots, duplication, dead code; address the top issues.

## Notes

- Single-step pipeline. Schedule via a service unit to run periodically.
