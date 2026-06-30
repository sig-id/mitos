# autofix — migration from chief-wiggum

This is chief-wiggum's autonomous-improvement service, re-expressed as a mitos **application**
(`spec/12`): a manifest + capabilities + pipelines, driven by a service unit.

## What migrated, and how the mechanism changed

### 1. Agents → capabilities (`spec/03`)
A chief-wiggum agent (`lib/agents/<area>/<name>.md` + optional `.sh`) became a **capability**
directory `capabilities/<name>/` with three files:
- `skill.md` — the SKILL pillar (formalized know-how; the old `<WIGGUM_SYSTEM_PROMPT>` body)
- `workload.json` — provider, template, trust tier, result pointer (the launch contract)
- `result.schema.json` — the RESULT artifact schema

The shell overrides (e.g. `quality-gate.sh`'s commit/push/PR actions) became **separate
capabilities** (`commit-push`, `open-pr`) rather than imperative post-decision hooks — git
publication is now a first-class pipeline stage with declared egress and vault-injected creds.

**Kept verbatim:** the RNG scope+concern selection in `random-audit/skill.md` — the weighted
`shuf` process, the 65 specific + 40 generic concerns, the applicability and recent-duplicate
rules. This is the heart of "how it picks which areas to audit."

### 2. The rigid pipeline → loose markdown (`spec/04 §8`, `spec/12 §1`)
chief-wiggum's `config/pipelines/*.json` was a **jump-FSM**: `steps[].on_result.{PASS|FIX|FAIL|SKIP: {jump|inline-agent}}`, `max`, `on_max`. That entire control surface is **replaced**:

| chief-wiggum (rigid)                         | mitos (loose)                                                            |
|----------------------------------------------|--------------------------------------------------------------------------|
| `on_result: { FIX: { inline agent } }`       | orchestrator `insert` Decision — open the paired fix capability          |
| `jump: self` / `jump: prev`                  | orchestrator `retry` with feedback (bounded by loop-manager signals)     |
| `jump: next` / linear order                  | orchestrator `advance`                                                   |
| `jump: abort`                                | orchestrator `abort`                                                     |
| `max` / `on_max`                             | budget (`04 §11`) + loop-manager escalation ladder (`04 §4.3–4.4`)        |
| `<result>PASS</result>` tag parsed for flow  | structured RESULT JSON (schema-validated); verdict read from the folded log, not a tag |
| `{{context_section}}` upstream injection     | shared-workspace artifact (`AUDIT_REPORT.json`) + `{{previous_results}}` |

A pipeline is now a markdown doc with a `Goal:`, an optional YAML frontmatter
(`budget`/`constraints`/`allowed_capabilities`/`success_criteria`/`checkpoints`), and a
`## Stages` list. The orchestrator reads it as **intent** and adapts; it is not compiled into
a state machine.

### 3. The forever-loop → a service unit (`spec/06 §8`)
The loop is **not** in the pipeline. chief-wiggum expressed the loop as `on_result` jumps
back to `random-audit`. In mitos:
- `pipelines/autofix.md` = **one cycle** (random-audit → verify-fix → quality-gate →
  commit-push → open-pr).
- `services/autofix-loop.json` = the **daemon** that re-fires that pipeline forever
  (`schedule: continuous`), up to `concurrency.max_instances: 4` in parallel, each in its own
  isolated workspace (= the old per-task git worktree). Restart policy, health checks, and
  conditions are declared here, systemd-style.

Service units are **tenant-side config**, not part of the app manifest (`spec/06 §11`), so
`services/autofix-loop.json` is an installable example — an operator installs it via the
control plane (`Mitos.ServiceRuntime.unit/2`).

## How pipeline stages pass data (the question)

Three mechanisms, no tags (`spec/04 §3.1, §4.2`, `spec/06 §4.1`, built in
`beam/lib/mitos/pipeline_runner.ex:106`):

1. **Shared workspace** — all steps in one run share one RW workspace. `random-audit` writes
   `AUDIT_REPORT.json`; `verify-fix` reads it. This is the primary channel for *structured* data.
2. **`{{previous_results}}`** — a text digest (`"step_name: summary"` per prior step) injected
   into each step's template bindings, plus `{{pipeline_step}}`. Built subset of the folded log.
3. **The folded event log** (spec target) — each step's `capability_finished` Effect carries a
   soft result + evidence refs; the orchestrator reads `fold(log)` to decide what's next.

## Pipeline inventory

| Pipeline | Entrypoint | What it does |
|---|---|---|
| `autofix` | `cycle` | one autonomous-improvement cycle (the loop body) |
| `code-review` | `review` | high-signal diff review (the original auto-code-review) |
| `default` | `implement` | plan→implement→audit→test→docs→validate |
| `fast` | — | lighter feature pipeline |
| `fix` | `fix-pr` | PR resolution lifecycle |
| `redteam` | `redteam` | security assessment |
| `multi-pr-resolve` | — | batch-coordinated PR resolution |
| `failure-recovery` | — | recover a failed task |
| `meta-todo-hunter` | `hunt-todos` | TODO/FIXME hunter |
| `meta-codebase-health` | `scan-health` | codebase health scan |
| `meta-self-improve` | — | improve guidance/memory |
| `intent-distill` | — | distill Intent DSL specs |
| `memory-extract` | — | distill a run into memory |

## Running it

```bash
# one cycle (manual)
curl -X POST localhost:7682/apps/autofix/entrypoints/cycle/runs \
  -H 'content-type: application/json' \
  -d '{"inputs": {}, "parameters": {"severity_floor": "MEDIUM"}}'

# a diff review
curl -X POST localhost:7682/apps/autofix/entrypoints/review/runs \
  -H 'content-type: application/json' \
  -d '{"inputs": {"diff_command": "git diff origin/main...HEAD"}}'

# the daemon — install the service unit via the control plane (tenant-side),
# then the drainer re-fires the autofix pipeline per the unit's schedule/concurrency.
```

## Status

- **Manifest:** loads cleanly (`app_id` computed); 37 capabilities, 13 pipelines, 7 entrypoints.
- **Core autofix loop** (`random-audit`, `verify-fix`, `quality-gate`, `commit-push`,
  `open-pr`, `code-review`): full skills + schemas, content ported from chief-wiggum.
- **Supporting capabilities** (planner, software-engineer, generic-fix, security-*, test-*,
  validation-review, git-* workflow, redteam, system/meta): proper manifests + schemas +
  condensed skills capturing role/procedure/success. Promote any to full prose as needed.
- **The orchestrator + loop-manager** (`spec/04 §3–4`) and the **work-pool disciplines /
  dependency graph** (`spec/06 §2, §4`) are the spec target the linear `PipelineRunner` is
  building toward; today the pipeline runs as an ordered stage list with `{{previous_results}}`
  + shared workspace, which is sufficient for the autofix cycle. The conditional fix-on-FIX
  logic is expressed as orchestrator intent in each pipeline's Notes, awaiting the full
  orchestrator to enact `insert`/`retry` Decisions.
