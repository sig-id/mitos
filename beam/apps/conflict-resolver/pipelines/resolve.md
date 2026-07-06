# Pipeline: resolve

Goal: resolve merge conflicts on one PR and push the fix. Linear per-PR; the
*monitoring* (which PR conflicted) is an event trigger outside this pipeline.

```yaml
budget: { usd: 2, wall_ms: 900000 }
```

## Stages

1. **conflict-fixer** — fetch the PR branch + the base branch, attempt the merge
   in a worktree, and resolve conflicts with the smallest safe change:
   - lockfile-only conflicts (package-lock, Cargo.lock, yarn.lock, flake.lock):
     accept the regenerated version (`package-manager install` / `cargo generate-lockfile` etc.);
   - trivial textual conflicts (whitespace, adjacent edits): merge both sides;
   - semantic conflicts (same lines changed differently): attempt a correct
     synthesis; if unsure, mark `manual` and stop.
   Run the repo's tests. On success, fast-forward the PR branch with the
   resolution commit and emit `RESOLUTION.json` `{ref, files_resolved, manual: []}`.
   The push itself is a sink crossing (GAPS.md).
