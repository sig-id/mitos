# Pipeline: audit-main

Goal: audit a commit range on main for security regressions, classify findings,
open issues for severe ones, and author fix PRs. The first run (no `from`) is a
broad baseline; subsequent runs continue from the last audited commit carried in
the tenant's security profile (07 memory).

```yaml
budget: { usd: 5, wall_ms: 1800000 }
```

## Stages

1. **repo-profiler** — (re)establish the security profile + audit scope from the
   repo's languages, frameworks, dependency manifests, and the prior profile
   (memory). On the first run this is a broad inventory; later runs update it as
   the project develops. Emits `profile.md` + writes long-term memory.

2. **diff-auditor** — compute the commit range to audit (`from..to`, or the full
   tree on baseline), then audit only what changed against the profile. Emits a
   findings list keyed by file:line with evidence (the diff hunk).

3. **finding-classifier** — classify each finding P0–P3 using the profile's
   severity rubric, dedupe against open issues, and emit `report.md` + a findings
   table. Drops below the `severity_floor` parameter.

4. **fix-author** — for findings at/above the severity floor, author a patch on a
   branch, write `AUDIT_REPORT.md`, and produce the issue + PR payload. (Opening
   the issue / pushing the PR is a sink crossing — see GAPS.md.)
