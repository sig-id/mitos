# Pipeline: code-review

Goal: a high-signal, read-only review of a single git diff. Single-step pipeline wrapping the
`code-review` capability.

```yaml
budget: { usd: 1, wall_ms: 600000 }
constraints: [ "read-only; do not edit, stage, commit, reset, or clean" ]
allowed_capabilities: [code-review]
success_criteria: [ "RESULT contains PASS|FIX|FAIL with cited findings" ]
```

## Stages

1. **code-review** — run the diff command, inspect changed files, emit the structured verdict.
