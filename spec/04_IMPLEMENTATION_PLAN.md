# IMPLEMENTATION_PLAN.md — Zig Implementation Workstreams

Status: draft v0.1  
Date: 2026-05-21  
Assumption: required Nucleus-side integration work is complete.

---

## 1. Direction

Mitos should be implemented in Zig.

The implementation should integrate `libghostty-vt` directly from a pinned Ghostty source tree or a stable upstream C ABI if one proves easier to maintain. It should not introduce another implementation language solely to call `libghostty-vt`.

Nucleus remains an out-of-process Linux sandbox backend. Mitos owns launch config generation, event ingestion, terminal parsing, policy, traces, and reports. Nucleus owns Linux isolation and sandbox lifecycle enforcement.

---

## 2. Shared Contracts First

Before parallel implementation starts, define skeleton modules and shared types:

```text
src/runtime/types.zig
src/terminal/core.zig
src/adapters/types.zig
src/policy/types.zig
src/sandbox/backend.zig
src/trace/types.zig
```

This first pass should avoid provider-specific or Nucleus-specific logic. Its purpose is to stabilize interfaces so the four workstreams can proceed without overlapping heavily.

Shared contracts:

- `RunId`, `RunState`, `RuntimeEvent`
- `TerminalCore`, `ScreenSnapshot`, `TerminalEffect`
- `AgentAdapter`, `AdapterRun`, `AdapterCapabilities`
- `PolicyDecision`, `ApprovalRequest`, `Action`
- `SandboxBackend`, `SandboxHandle`, `SandboxEvent`
- `TraceWriter`, artifact paths, JSONL event format

---

## 3. Agent 1: Terminal And PTY Core

Owned paths:

```text
src/terminal/
src/pty/
third_party/ghostty/
test/fixtures/terminal/
```

Responsibilities:

- Wire `build.zig` to a pinned Ghostty / `libghostty-vt` source or compatible prebuilt artifact.
- Implement `src/terminal/ghostty.zig` behind the product-facing `TerminalCore` API.
- Implement raw PTY byte ingestion, resize handling, key/paste encoding, visible text, scrollback text, and screen snapshots.
- Implement Unix PTY support first; leave Windows ConPTY behind the same abstraction.
- Add golden tests for ANSI colors, cursor movement, alternate screen, resize/reflow, Unicode, and approval-like screens.

Deliverables:

- `TerminalCore` works independently of provider adapters.
- Terminal fixtures produce deterministic `ScreenSnapshot` output.
- Raw `libghostty` symbols do not leak outside `src/terminal/ghostty.zig`.

---

## 4. Agent 2: Runtime, Policy, Trace, And Reports

Owned paths:

```text
src/runtime/
src/policy/
src/approvals/
src/trace/
src/redaction/
src/report/
test/fixtures/runtime/
```

Responsibilities:

- Implement run lifecycle orchestration: create, prepare, run, finalize, fail, cancel.
- Implement event fan-out from runtime components into the trace store.
- Implement policy evaluation for allow/ask/block actions.
- Implement approval request persistence and decision routing.
- Implement redaction for terminal text, provider events, and generated reports.
- Generate initial run reports from event logs, diffs, approvals, sandbox events, and final output.

Deliverables:

- A fake run can produce a complete `.runs/<run_id>/` directory.
- Reports are generated from trace data rather than live runtime state.
- Policy decisions are deterministic and auditable.

---

## 5. Agent 3: Provider Adapters And Prompt Detection

Owned paths:

```text
src/adapters/
src/providers/
src/prompt/
test/fixtures/providers/
```

Responsibilities:

- Implement the adapter registry and adapter detection.
- Implement one fake provider adapter for integration tests.
- Implement one real headless adapter first.
- Implement one real PTY adapter using `TerminalCore`.
- Implement provider-specific prompt detectors for approval requests, final-answer detection, and error screens.
- Add recorded-session fixtures for each provider adapter before broadening support.

Deliverables:

- Adapter selection follows configured priority order.
- Headless and PTY adapters emit the same high-level runtime event types.
- Prompt detection never injects terminal input directly; it only emits semantic events.

---

## 6. Agent 4: Workspace, Nucleus Sandbox, Build, And Packaging

Owned paths:

```text
src/workspace/
src/sandbox/
src/config/
build.zig
build.zig.zon
test/fixtures/sandbox/
```

Responsibilities:

- Implement project config parsing and validation.
- Implement per-run Git worktree creation, cleanup, pre-run diff, and post-run diff.
- Implement the Nucleus backend adapter against `spec/03_NUCLEUS_INTEGRATION.md`.
- Generate Nucleus strict-agent launch configs from mitos policy/config.
- Ingest Nucleus JSONL lifecycle/resource/audit events into `RuntimeEvent`.
- Implement clear version checks for the supported Nucleus contract.
- Set up `zig build`, `zig build test`, release builds, and packaging metadata.

Deliverables:

- A fake provider can run inside Nucleus strict-agent mode on Linux.
- Sandbox events are persisted to `sandbox.events.jsonl` and normalized into runtime events.
- Build and test commands work with the Zig toolchain only.

---

## 7. Integration Order

1. Merge shared contracts and skeleton modules.
2. Merge terminal/PTY fixtures and fake runtime traces.
3. Merge workspace and Nucleus fake-backend tests.
4. Run the fake provider end-to-end without Nucleus.
5. Run the fake provider end-to-end inside Nucleus.
6. Add one real headless provider.
7. Add one real PTY provider.
8. Harden reports, redaction, config errors, and packaging.

Each workstream should add fixtures at the boundary it owns so later integrations fail with clear diffs rather than live-provider ambiguity.
