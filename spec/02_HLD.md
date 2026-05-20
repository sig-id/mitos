# HLD.md — Agent Terminal Runtime

Status: draft v0.1  
Date: 2026-05-20  
Core terminal dependency: **`libghostty` / `libghostty-vt`**  
Primary implementation language: **Zig**, integrating `libghostty-vt` directly behind a narrow internal terminal module.

---

## 1. Executive summary

Agent Terminal Runtime is a local-first runtime for executing AI coding agents and provider CLIs under policy control.

The runtime supports two execution families:

1. **Structured execution** through provider APIs or provider headless CLI modes.
2. **Interactive execution** through a pseudoterminal, using `libghostty-vt` to parse VT sequences and maintain terminal screen state.

The system is intentionally designed so `libghostty` is an internal terminal-core dependency, not the top-level product abstraction.

The top-level abstraction is:

```text
Agent Run → Agent Session → Events → Policy Decisions → Artifacts
```

The terminal layer is one way of observing and controlling an agent session.

---

## 2. Architectural principles

### 2.1 Headless first, PTY second

Use provider-native structured modes whenever possible.

Priority order:

```text
1. Native API / SDK adapter
2. Headless CLI adapter with JSON or stream output
3. Interactive CLI over PTY
4. Raw shell adapter
```

PTY automation is powerful but brittle. It is the universal fallback, not the preferred semantic interface.

### 2.2 Terminal is observability, not security

A terminal controls input/output. It does not fully control filesystem access, network access, subprocesses, or credentials.

Therefore:

- `libghostty` handles terminal-state correctness.
- The sandbox layer handles authority.
- The policy engine handles approval.
- The trace layer handles accountability.

### 2.3 Wrap unstable dependencies

`libghostty-vt` should be wrapped behind a narrow internal interface because its public API may evolve.

No product component outside `src/terminal/ghostty.zig` should import raw `libghostty` symbols.

### 2.4 Local-first by default

Runs, logs, diffs, and approvals are stored locally. Remote model calls for watcher/classifier functionality are opt-in.

### 2.5 Event-sourced runtime

Every meaningful event is appended to a local event log. Reports, replay views, summaries, and audits are derived from that log.

---

## 3. High-level system diagram

```text
┌──────────────────────────────────────────────────────────────────┐
│                          User Surfaces                            │
│                                                                  │
│   CLI: atr run       Local Web UI       Future Native App         │
└───────────────┬─────────────────────┬────────────────────────────┘
                │                     │
                ▼                     ▼
┌──────────────────────────────────────────────────────────────────┐
│                         Runtime API                               │
│          local daemon or direct in-process CLI runtime             │
└────────────────────────────┬─────────────────────────────────────┘
                             ▼
┌──────────────────────────────────────────────────────────────────┐
│                       Session Manager                             │
│                                                                  │
│  creates runs, resolves providers, owns lifecycle, streams events  │
└───────────────┬─────────────────────────────┬────────────────────┘
                │                             │
                ▼                             ▼
┌──────────────────────────────┐    ┌───────────────────────────────┐
│       Adapter Registry        │    │        Policy Engine           │
│                              │    │ allow / ask / block decisions  │
│ API / headless / PTY adapters │    └──────────────┬────────────────┘
└───────────────┬──────────────┘                   │
                │                                  ▼
                │                     ┌───────────────────────────────┐
                │                     │      Approval Controller       │
                │                     │ human UI / local watcher model │
                │                     └───────────────────────────────┘
                ▼
┌──────────────────────────────────────────────────────────────────┐
│                        Execution Layer                            │
│                                                                  │
│  Headless process runner        PTY runner + terminal core         │
│                                 ┌─────────────────────────────┐    │
│                                 │ libghostty-vt wrapper        │    │
│                                 │ screen state / scrollback    │    │
│                                 │ VT parsing / snapshots       │    │
│                                 └─────────────────────────────┘    │
└───────────────┬─────────────────────────────┬────────────────────┘
                │                             │
                ▼                             ▼
┌──────────────────────────────┐    ┌───────────────────────────────┐
│       Sandbox Layer           │    │        Observability           │
│ Nucleus backend / workspace / │    │ fs watcher / git diff /        │
│ env / process / network       │    │ process monitor / redactor     │
└───────────────┬──────────────┘    └──────────────┬────────────────┘
                │                                  │
                ▼                                  ▼
┌──────────────────────────────────────────────────────────────────┐
│                         Trace Store                               │
│ events.jsonl, terminal.raw, snapshots, diff.patch, approvals,      │
│ commands, files_changed, test_results, final.md                    │
└──────────────────────────────────────────────────────────────────┘
```

---

## 4. Main components

## 4.1 CLI

Binary name for MVP:

```text
atr
```

Primary commands:

```bash
atr run --provider <provider> --task <text>
atr run --provider <provider> --task-file <file>
atr compare --providers a,b,c --task <text>
atr report <run_id>
atr replay <run_id>
atr providers list
atr policy check <command-or-action>
```

Responsibilities:

- Parse user command.
- Load project and user config.
- Start runtime directly or connect to local daemon.
- Display live output or progress.
- Request approvals.
- Print final report path.

---

## 4.2 Runtime API

Can run in two modes:

```text
1. Direct mode: CLI starts runtime in-process.
2. Daemon mode: CLI connects to local runtime daemon over Unix socket / named pipe.
```

MVP should start with direct mode. Daemon mode is useful for long-running UI sessions later.

Core operations:

```zig
pub const RuntimeApi = struct {
    start_run: *const fn (*RuntimeApi, StartRunRequest) anyerror!RunId,
    subscribe: *const fn (*RuntimeApi, RunId) EventStream,
    approve: *const fn (*RuntimeApi, ApprovalId, ApprovalDecision) anyerror!void,
    cancel_run: *const fn (*RuntimeApi, RunId) anyerror!void,
    get_report: *const fn (*RuntimeApi, RunId) anyerror!RunReport,
};
```

---

## 4.3 Session Manager

The Session Manager owns the run lifecycle.

Responsibilities:

- Create run ID.
- Create workspace.
- Resolve provider adapter.
- Start execution.
- Route events to trace store.
- Route approval requests to policy engine.
- Handle cancellation and cleanup.
- Finalize artifacts.

Lifecycle:

```text
Created → PreparingWorkspace → StartingAdapter → Running → Finalizing → Completed
                                                       └→ Failed
                                                       └→ Cancelled
```

---

## 4.4 Provider Adapter Registry

The adapter registry resolves the best adapter for the requested provider.

```zig
pub const AgentAdapter = struct {
    provider: ProviderId,
    capabilities: AdapterCapabilities,
    start: *const fn (*AgentAdapter, AdapterContext, Task) anyerror!AdapterHandle,
};

pub const AdapterCapabilities = struct {
    supports_headless: bool,
    supports_json_output: bool,
    supports_streaming_events: bool,
    supports_interactive_pty: bool,
    supports_cost_reporting: bool,
    supports_model_selection: bool,
};
```

Adapter selection:

```text
requested provider + project config + installed CLI version
    → candidate adapters
    → filter by capability
    → choose highest-priority available adapter
```

Example config:

```yaml
providers:
  claude:
    adapter_order: [headless_json, pty]
  codex:
    adapter_order: [exec_json, pty]
  gemini:
    adapter_order: [headless_json, pty]
```

---

## 4.5 Headless Adapter

Used for provider commands that finish without an interactive TUI.

Examples:

```text
Claude Code: print/headless mode with structured output where available
Codex CLI: codex exec
Gemini CLI: gemini --prompt / -p with text or JSON output
```

Responsibilities:

- Build command argv.
- Set working directory.
- Set environment.
- Capture stdout/stderr.
- Parse JSON or stream JSON when available.
- Convert provider output into runtime events.
- Return final output and exit status.

Data flow:

```text
Task → HeadlessAdapter → ProcessRunner → stdout/stderr parser → Runtime Events
```

---

## 4.6 PTY Adapter

Used for interactive provider CLIs.

Responsibilities:

- Open pseudoterminal.
- Spawn provider CLI attached to PTY slave.
- Feed raw PTY bytes to:
  - raw log recorder
  - `libghostty` terminal core
  - prompt detector
- Inject input into PTY master.
- Resize terminal.
- Handle signals.
- Kill process tree on cancellation.

Data flow:

```text
Provider CLI output
  → PTY master bytes
  → raw log tee
  → libghostty-vt write()
  → screen snapshot / scrollback / visible text
  → prompt detector
  → policy engine
  → input injector
  → PTY master
```

---

## 4.7 Terminal core: `libghostty-vt`

### 4.7.1 Why `libghostty-vt`

The PTY adapter must not parse terminal output with only regex or ANSI stripping. Provider CLIs may use:

- Cursor movement.
- Alternate screen.
- Rich styles.
- Resize/reflow.
- Unicode grapheme clusters.
- Hyperlinks.
- Mouse/keyboard protocols.
- Dynamic approval screens.

`libghostty-vt` provides a real terminal-state engine that parses VT sequences and maintains screen state.

### 4.7.2 Integration boundary

Create an internal Zig module:

```text
src/terminal/ghostty.zig
```

This module is the only code that directly links to `libghostty-vt`.

Public Zig-facing interface:

```zig
pub const TerminalCore = struct {
    write_from_pty: *const fn (*TerminalCore, []const u8) TerminalError!TerminalEffects,
    resize: *const fn (*TerminalCore, u16, u16, CellSize) TerminalError!void,
    snapshot: *const fn (*TerminalCore, std.mem.Allocator) TerminalError!ScreenSnapshot,
    visible_text: *const fn (*TerminalCore, std.mem.Allocator) TerminalError![]u8,
    scrollback_text: *const fn (*TerminalCore, std.mem.Allocator, usize) TerminalError![]u8,
    encode_key: *const fn (*TerminalCore, KeyInput, std.mem.Allocator) TerminalError![]u8,
    encode_paste: *const fn (*TerminalCore, []const u8, std.mem.Allocator) TerminalError![]u8,
};
```

`libghostty` types must not leak beyond this module.

### 4.7.3 Screen snapshot model

```zig
pub const ScreenSnapshot = struct {
    cols: u16,
    rows: u16,
    cursor: CursorState,
    title: ?[]const u8,
    alternate_screen: bool,
    cells: []const Cell,
    timestamp_ms: i64,
};

pub const Cell = struct {
    x: u16,
    y: u16,
    text: []const u8,
    style: CellStyle,
};

pub const CellStyle = struct {
    bold: bool,
    italic: bool,
    underline: bool,
    inverse: bool,
    fg: ?Color,
    bg: ?Color,
};
```

### 4.7.4 Terminal effects

When processing PTY bytes, the terminal core may emit effects:

```zig
pub const TerminalEffect = union(enum) {
    bell,
    title_changed: []const u8,
    write_to_pty: []const u8,
    clipboard_request,
    clipboard_set: []const u8,
    screen_changed,
};
```

Security-sensitive effects, especially clipboard and paste effects, must be routed through policy.

### 4.7.5 Build strategy

Recommended initial approach:

```text
third_party/ghostty/        pinned Git submodule or vendored source
src/terminal/ghostty.zig    direct libghostty-vt integration
src/terminal/core.zig       product-facing terminal API
build.zig                   builds or links the selected libghostty-vt objects
```

Guidelines:

- Pin a known working Ghostty commit.
- Keep a compatibility matrix in `src/terminal/COMPAT.md`.
- Prefer direct Zig integration over a secondary language bridge. Use a C ABI shim only if upstream `libghostty-vt` exposes a stable boundary that is easier to maintain.
- Run golden tests against terminal byte sequences.
- Treat `libghostty-vt` API churn as an integration risk.

---

## 4.8 Prompt Detector

The Prompt Detector turns terminal state into semantic events.

Inputs:

- Current visible screen text.
- Recent scrollback.
- Raw byte window.
- Provider adapter identity.
- Provider CLI version.
- Process state.
- Filesystem/process events.

Outputs:

```zig
pub const PromptEvent = union(enum) {
    approval_requested: ApprovalRequest,
    question_asked: ContextQuestion,
    final_answer_candidate: []const u8,
    idle_detected,
    error_detected: []const u8,
};
```

Detection layers:

```text
1. Provider-specific deterministic parser
2. Generic deterministic prompt patterns
3. Optional local model classifier
4. Human fallback
```

The detector should never directly inject input. It only emits events.

---

## 4.9 Policy Engine

The Policy Engine decides whether an action is allowed.

```zig
pub const PolicyEngine = struct {
    evaluate: *const fn (*PolicyEngine, Action, PolicyContext) PolicyDecision,
};

pub const PolicyDecision = union(enum) {
    allow: struct { rule_id: RuleId },
    ask: struct { rule_id: ?RuleId, reason: []const u8 },
    block: struct { rule_id: ?RuleId, reason: []const u8 },
};
```

Action categories:

```zig
pub const Action = union(enum) {
    read_path: []const u8,
    write_path: []const u8,
    delete_path: []const u8,
    run_command: struct { argv: []const []const u8, cwd: []const u8 },
    network_access: struct { destination: ?[]const u8 },
    install_package: struct { manager: []const u8, package: []const u8 },
    git_push: struct { remote: []const u8, branch: []const u8 },
    read_secret: struct { path_or_env: []const u8 },
    terminal_input: struct { bytes: []const u8, reason: []const u8 },
};
```

Policy sources:

```text
1. Built-in default policy
2. User policy
3. Project policy
4. Per-run overrides
```

Precedence:

```text
explicit block > explicit ask > explicit allow > default
```

---

## 4.10 Approval Controller

The Approval Controller mediates between policy decisions and terminal input.

Responsibilities:

- Create approval request.
- Present request to CLI/UI.
- Optionally ask local watcher model for explanation, not authority.
- Apply user decision.
- Inject approved input into PTY if needed.
- Persist decision.

Approval request structure:

```zig
pub const ApprovalRequest = struct {
    id: ApprovalId,
    run_id: RunId,
    action: Action,
    source: ApprovalSource,
    risk: RiskLevel,
    terminal_context: ?ScreenSnapshot,
    matched_rule: ?RuleId,
    suggested_decision: ?PolicyDecision,
};
```

---

## 4.11 Sandbox Layer

The sandbox protects the user’s machine from the child process.

Stage 1 MVP sandbox:

- Git worktree per run.
- Clean environment variable allowlist.
- Working directory locked to `/workspace`.
- Pre-run and post-run Git diff.
- Process-tree cleanup.
- Linux: Nucleus strict-agent backend is the default enforced sandbox.
- Nucleus emits sandbox lifecycle/resource/audit events into the trace.

Portable fallback sandbox:

- macOS: Seatbelt-style sandbox profile where available.
- Windows: Job objects + restricted token, or WSL/container backend.
- File allowlist / denylist.
- Network warning where enforcement is unavailable.

High-risk sandbox:

- Nucleus gVisor runtime for untrusted Linux workloads.
- VM runner for high-risk tasks.
- Remote ephemeral runner.
- Deterministic cache mounts.

The sandbox layer must be independent of `libghostty`.

### 4.11.1 Nucleus backend

Nucleus is the completed Linux sandbox backend.

The detailed contract lives in `spec/03_NUCLEUS_INTEGRATION.md`.

Mitos invokes Nucleus through a stable launch contract:

```text
Mitos RunRequest
  → Nucleus strict-agent config
  → process running in /workspace
  → PTY/stdio stream back to mitos
  → Nucleus JSONL events back to TraceStore
```

Nucleus responsibilities:

- Create Linux namespaces.
- Apply cgroup resource limits.
- Apply seccomp and Landlock policy.
- Drop capabilities.
- Mount the run worktree at `/workspace`.
- Mount a sandbox home at `/home/agent`.
- Mount explicit provider credentials under `/run/secrets`.
- Enforce network mode and egress policy.
- Attach PTY/console for interactive provider CLIs.
- Emit sandbox lifecycle, audit, and resource events.

Mitos responsibilities:

- Create and diff the Git worktree.
- Build the provider command and environment.
- Decide policy approvals.
- Parse terminal output through `libghostty-vt`.
- Detect provider prompts.
- Inject approved terminal input.
- Redact and persist run artifacts.

Zig-facing abstraction:

```zig
pub const SandboxBackend = struct {
    prepare: *const fn (*SandboxBackend, SandboxRequest) anyerror!SandboxHandle,
    spawn: *const fn (*SandboxBackend, *SandboxHandle, SandboxCommand) anyerror!SandboxProcess,
    attach_pty: *const fn (*SandboxBackend, *SandboxHandle, PtySize) anyerror!SandboxPty,
    events: *const fn (*SandboxBackend, *SandboxHandle) EventStream,
    terminate: *const fn (*SandboxBackend, *SandboxHandle, TerminationReason) anyerror!void,
};
```

The default Linux launch profile:

```yaml
sandbox:
  backend: nucleus
  mode: strict-agent
  runtime: gvisor
  workspace_mount: /workspace
  workdir: /workspace
  home: /home/agent
  network: none
  resources:
    memory: 4G
    cpus: 4
    pids: 1024
```

When a provider requires network access, mitos must translate policy into an explicit Nucleus network profile before launch. Network access cannot be granted only by answering a terminal prompt.

Non-Linux backends must implement the same high-level contract but may provide weaker enforcement until platform-specific hardening is complete.

---

## 4.12 Observability Layer

Responsibilities:

- Capture raw PTY bytes.
- Capture normalized terminal text.
- Capture screen snapshots.
- Watch filesystem changes.
- Capture Git diff.
- Monitor process tree.
- Capture command attempts where observable.
- Redact secrets.
- Emit events.

Important limitation:

The runtime cannot reliably infer every command from terminal output alone. It should combine:

- Provider structured events when available.
- Shell integration if configured.
- Process monitoring.
- Terminal text heuristics.
- Filesystem/git observers.

---

## 4.13 Trace Store

Trace store layout:

```text
.runs/
  <run_id>/
    run.yaml
    events.jsonl
    terminal.raw
    terminal.redacted.txt
    terminal.snapshots.jsonl
    sandbox.events.jsonl
    resources.jsonl
    final.md
    diff.patch
    files_changed.json
    commands.jsonl
    approvals.jsonl
    test_results.jsonl
    artifacts/
```

### 4.13.1 Event schema

```zig
pub const RuntimeEvent = union(enum) {
    run_started: RunStarted,
    workspace_created: WorkspaceCreated,
    sandbox_prepared: SandboxPrepared,
    sandbox_event: SandboxEvent,
    resource_sample: ResourceSample,
    adapter_selected: AdapterSelected,
    process_spawned: ProcessSpawned,
    pty_bytes: PtyBytesMeta,
    screen_updated: ScreenUpdated,
    prompt_detected: PromptDetected,
    approval_requested: ApprovalRequested,
    approval_resolved: ApprovalResolved,
    file_changed: FileChanged,
    command_observed: CommandObserved,
    git_diff_captured: GitDiffCaptured,
    headless_output_received: HeadlessOutputReceived,
    final_answer_detected: FinalAnswerDetected,
    run_completed: RunCompleted,
    run_failed: RunFailed,
};
```

Raw PTY bytes should generally be stored outside JSONL because they are binary and high-volume. `PtyBytes` events should reference offsets into `terminal.raw`.

Example:

```json
{
  "type": "PtyBytes",
  "run_id": "01HXYZ",
  "timestamp": "2026-05-20T18:00:01.123Z",
  "offset": 1024,
  "len": 4096
}
```

---

## 4.14 Redaction

Redaction must run before logs are displayed, exported, or sent to any model.

Redaction sources:

- Known environment variables.
- `.env` files.
- User-provided secret patterns.
- Common credential regexes.
- Provider-specific token formats.
- Cloud credential patterns.

Redaction outputs:

```text
terminal.raw             raw, local only, never export by default
terminal.redacted.txt    safe-ish display text
events.jsonl             should not contain raw secrets
export.zip               redacted only unless user overrides
```

---

## 4.15 Watcher model

Optional local or remote model that helps interpret terminal state and context questions.

Default:

```yaml
watcher:
  enabled: false
  mode: local_only
```

Allowed uses:

- Summarize run progress.
- Classify ambiguous prompts.
- Explain approval risk.
- Draft answer to context questions.

Disallowed by default:

- Auto-approve high-risk actions.
- Send secrets to remote models.
- Modify policy without user approval.

Recommended model-routing rule:

```text
local watcher may see redacted terminal + repository snippets
remote watcher may see only explicit user-approved context
```

---

## 5. Execution flows

## 5.1 Headless execution flow

```text
User runs atr
  → CLI loads config
  → Session Manager creates run
  → Workspace Manager creates git worktree
  → Sandbox Backend prepares Nucleus strict-agent container
  → Adapter Registry selects headless adapter
  → Headless Adapter spawns provider command inside sandbox
  → stdout/stderr parsed into events
  → Observability captures diff/files/tests
  → Report Generator writes final.md
  → CLI prints summary
```

Sequence:

```text
CLI → SessionManager: start_run(task, provider)
SessionManager → WorkspaceManager: create_worktree()
SessionManager → SandboxBackend: prepare_nucleus(workspace, policy)
SessionManager → AdapterRegistry: select(provider, headless preferred)
Adapter → SandboxBackend: spawn(argv, cwd=/workspace, env)
ProcessRunner → Adapter: stdout/stderr chunks
Adapter → TraceStore: HeadlessOutputReceived events
SandboxBackend → TraceStore: SandboxEvent / ResourceSample events
SessionManager → GitObserver: capture_diff()
SessionManager → ReportGenerator: finalize()
```

---

## 5.2 PTY execution flow

```text
User runs atr with PTY adapter
  → Session Manager creates workspace
  → Sandbox Backend prepares Nucleus strict-agent container
  → PTY Runner opens PTY or connects to Nucleus console socket
  → Provider CLI starts in PTY inside sandbox
  → PTY bytes flow into libghostty-vt
  → Terminal snapshots update
  → Prompt Detector emits approval request
  → Policy Engine evaluates action
  → Approval Controller asks user or applies rule
  → Input Injector writes approved input to PTY
  → Run completes
  → Diff/report generated
```

Sequence:

```text
PTYRunner → RawLog: append bytes
PTYRunner → GhosttyTerminal: write_from_pty(bytes)
GhosttyTerminal → PromptDetector: snapshot + visible text
SandboxBackend → TraceStore: SandboxEvent / ResourceSample events
PromptDetector → PolicyEngine: action candidate
PolicyEngine → ApprovalController: ask/allow/block
ApprovalController → PTYRunner: inject input if allowed
```

---

## 5.3 Approval flow

```text
Prompt detected: "Run npm install? [y/N]"
  → Prompt Detector creates Action::InstallPackage
  → Policy Engine returns Ask
  → Approval Controller displays request
  → User chooses Deny
  → Input Injector sends "n\n"
  → Event log records decision
```

Important rule:

> The runtime must never let the prompt detector itself decide to press keys. All injected input must be traceable to either deterministic policy or explicit user approval.

---

## 6. Runtime module layout

Recommended Zig project layout:

```text
agent-terminal-runtime/
  build.zig
  build.zig.zon
  src/
    main.zig                 # CLI binary
    runtime/                 # session manager + runtime API
    adapters/                # provider adapter contracts + registry
    providers/
      claude.zig             # provider-specific adapter
      codex.zig
      gemini.zig
    pty/                     # Unix PTY + Windows ConPTY abstraction
    terminal/
      core.zig               # product-facing terminal API
      ghostty.zig            # direct libghostty-vt integration
      snapshot.zig
      COMPAT.md
    policy/                  # rules, actions, policy engine
    approvals/               # approval controller
    workspace/               # git worktree manager
    sandbox/
      backend.zig            # sandbox abstraction
      nucleus.zig            # Linux Nucleus backend
    observability/           # fs/process/git observers
    trace/                   # event log + artifact writer
    redaction/               # secret redaction
    report/                  # final report generator
    watcher/                 # optional local/remote model classifier
    fixtures/                # fake CLIs, golden terminal streams
  third_party/
    ghostty/                 # pinned source or submodule
```

---

## 7. `libghostty` Zig integration design

## 7.1 Binding strategy

Mitos should use Zig-native integration first.

Recommended approach:

- Pin Ghostty source under `third_party/ghostty/`.
- Build or link the required `libghostty-vt` objects from `build.zig`.
- Keep all direct Ghostty calls in `src/terminal/ghostty.zig`.
- Expose only owned, product-level terminal types from `src/terminal/core.zig`.

A C ABI shim is acceptable only if upstream `libghostty-vt` exposes a stable C boundary that is simpler to maintain than direct Zig integration. Do not introduce another implementation language solely to call `libghostty-vt`.

Tradeoffs:

- Direct Zig integration matches Ghostty’s implementation language and avoids unnecessary language boundaries.
- A narrow C ABI can be useful for version isolation if upstream stabilizes it.
- Either path must preserve the same product-facing `TerminalCore` contract.

---

## 7.2 Safe wrapper contract

`src/terminal/ghostty.zig` should expose Zig methods that own all terminal handles and allocator-backed snapshots.

```zig
pub const GhosttyTerminal = struct {
    raw: *ghostty.Terminal,
    allocator: std.mem.Allocator,
    cols: u16,
    rows: u16,

    pub fn writeFromPty(self: *GhosttyTerminal, bytes: []const u8) TerminalError!TerminalEffects {
        // call libghostty-vt here only
    }

    pub fn snapshot(self: *GhosttyTerminal) TerminalError!ScreenSnapshot {
        // convert libghostty cell/state into owned Zig types
    }

    pub fn deinit(self: *GhosttyTerminal) void {
        // free libghostty terminal and owned buffers
    }
};
```

Safety rules:

- All raw `libghostty` handles are owned by `src/terminal/ghostty.zig`.
- No borrowed data from `libghostty` escapes beyond the wrapper call lifetime.
- All snapshots are converted into allocator-owned Zig data.
- All functions that can fail return Zig error unions.
- Terminal calls are fuzzed/golden-tested with terminal byte streams.

---

## 7.3 Terminal golden tests

Golden tests should cover:

- ANSI colors.
- Cursor movement.
- Alternate screen.
- Line clearing.
- Progress spinners.
- Unicode graphemes.
- Terminal resize and reflow.
- Approval prompt screens.
- Provider-specific sample sessions.

Test form:

```text
input: raw PTY byte stream
expected: visible text + cell styles + cursor state + normalized prompt
```

---

## 8. Provider adapters

## 8.1 Generic adapter interface

```zig
pub const AgentAdapter = struct {
    id: AdapterId,
    provider: ProviderId,
    vtable: *const VTable,

    pub const VTable = struct {
        detect: *const fn (*AgentAdapter) anyerror!AdapterDetection,
        capabilities: *const fn (*AgentAdapter) AdapterCapabilities,
        start: *const fn (*AgentAdapter, AdapterContext, Task) anyerror!AdapterRun,
    };
};

pub const AdapterRun = struct {
    event_stream: EventStream,
    send_input: *const fn (*AdapterRun, AdapterInput) anyerror!void,
    cancel: *const fn (*AdapterRun) anyerror!void,
};
```

## 8.2 Detection

Each adapter should detect:

- CLI binary path.
- CLI version.
- Auth status if safely available.
- Supported flags.
- Headless support.
- JSON/stream output support.

Example:

```zig
pub const AdapterDetection = struct {
    available: bool,
    binary: ?[]const u8,
    version: ?[]const u8,
    auth_status: AuthStatus,
    warnings: []const []const u8,
};
```

## 8.3 Provider-specific prompt detectors

Each provider adapter may register prompt detectors.

```zig
pub const ProviderPromptDetector = struct {
    detect: *const fn (*ProviderPromptDetector, ScreenSnapshot, PromptContext, std.mem.Allocator) anyerror![]PromptEvent,
};
```

Provider-specific detectors should be tested against recorded sessions.

---

## 9. Sandbox and workspace design

## 9.1 Workspace creation

Default workspace mode:

```text
git worktree add .agent-worktrees/<run_id> <base_branch>
```

Benefits:

- Isolates changes.
- Keeps original repo clean.
- Makes diff capture easy.
- Supports parallel runs.

On Linux, the worktree is mounted into Nucleus at:

```text
/workspace
```

The provider process always starts with `cwd=/workspace`. Host paths should not be exposed to provider CLIs except through explicit Nucleus mounts.

## 9.2 Environment policy

Default environment should be minimal.

Allowlist examples:

```text
HOME             set to sandbox home when possible
PATH             controlled path
SHELL            controlled shell
TERM             xterm-ghostty or xterm-256color-compatible value
LANG             inherited
LC_*             inherited
```

Denylist examples:

```text
OPENAI_API_KEY
ANTHROPIC_API_KEY
GEMINI_API_KEY
AWS_*
GCP_*
AZURE_*
SSH_AUTH_SOCK
GITHUB_TOKEN
```

Provider CLIs may need credentials. The runtime must support explicit credential pass-through per provider rather than inheriting all environment variables.

Linux default:

```text
HOME             /home/agent
WORKSPACE        /workspace
RUN_ID           <run_id>
```

Secrets are mounted through Nucleus under `/run/secrets` and exposed to provider CLIs only through provider-specific adapter configuration. Mitos should not pass sensitive values through argv.

## 9.3 File access

Nucleus default file layout:

```text
/workspace       writable run worktree
/context         optional read-only supplemental context
/home/agent      sandbox home
/run/secrets     explicit provider credentials
/tmp             writable scratch
```

Default access policy:

- `/workspace`: read/write, execute when the selected toolchain requires running build or test outputs.
- `/context`: read-only.
- `/home/agent`: read/write.
- `/run/secrets`: read-only and provider-scoped.
- Host private paths such as `~/.ssh`, cloud credentials, and host config directories are never mounted by default.

Mitos policy evaluates semantic actions before launch and during runtime. Nucleus enforces the kernel-level allowlist that mitos derives from that policy.

## 9.4 Network policy

Default Linux network mode:

```text
network: none
```

If a provider adapter requires API access, mitos must request a Nucleus launch profile with explicit egress. Runtime approval prompts cannot widen kernel network policy after launch unless the run is restarted with a new sandbox profile.

Network policy translation:

```text
Mitos Action::NetworkAccess(destination)
  → provider/domain allowlist
  → Nucleus network mode + egress policy
  → SandboxPrepared event records effective policy
```

## 9.5 Nucleus event ingestion

Nucleus emits JSONL events for lifecycle and enforcement state. Mitos maps them into runtime events:

```text
nucleus container_start        → SandboxPrepared
nucleus cgroup_created         → SandboxEvent
nucleus seccomp_applied        → SandboxEvent
nucleus landlock_applied       → SandboxEvent
nucleus capabilities_dropped   → SandboxEvent
nucleus egress_policy_applied  → SandboxEvent
nucleus resource_sample        → ResourceSample
nucleus container_stop         → SandboxEvent
```

Nucleus stdout/stderr or PTY bytes remain workload output. Nucleus audit events must be carried on a separate event file descriptor, sidecar JSONL file, or other machine-readable control stream.

---

## 10. Security boundaries

## 10.1 Trusted components

Trusted:

- Runtime binary.
- Policy engine.
- Trace store.
- Sandbox setup.
- `src/terminal/ghostty.zig` wrapper.

Untrusted or semi-trusted:

- Provider CLI.
- Provider model output.
- Generated code.
- Shell commands run by the provider CLI.
- Terminal escape sequences.
- Watcher model classification.

## 10.2 Terminal escape handling

Terminal escape sequences are untrusted input.

Rules:

- Do not allow terminal output to write to clipboard without policy approval.
- Do not execute OSC or custom sequences outside the terminal core without validation.
- Sanitize exported terminal text.
- Keep raw terminal logs local by default.

## 10.3 Prompt injection

The agent may read malicious project files that instruct it to request dangerous actions.

Mitigation:

- Policy rules are external to model output.
- Watcher model suggestions are advisory only.
- Dangerous actions require deterministic policy or human approval.
- Reports mark which decisions were model-assisted.

---

## 11. Reporting

Final report sections:

```text
# Run Report

## Summary
- Task
- Provider
- Adapter
- Status
- Duration
- Workspace
- Base commit
- Head commit

## Final answer

## Files changed

## Diff summary

## Commands observed

## Approvals

## Tests / validation

## Warnings

## Artifacts
```

Report generation reads from `events.jsonl`, Git diff, and final output.

---

## 12. Failure modes and mitigations

### 12.1 Provider CLI update breaks prompt detection

Mitigation:

- Provider version detection.
- Adapter compatibility warnings.
- Recorded fixture tests.
- Fallback to human approval.

### 12.2 `libghostty` API changes

Mitigation:

- Pinned commit.
- Narrow wrapper.
- Compatibility test suite.
- No raw API leakage.

### 12.3 Runtime hangs waiting for prompt

Mitigation:

- Idle detection.
- Timeout rules.
- User-visible “waiting” state.
- Cancel support.
- Last screen snapshot in report.

### 12.4 Child process escapes workspace

Mitigation:

- Minimal env.
- Worktree isolation.
- Nucleus strict-agent sandbox on Linux.
- Nucleus Landlock/seccomp/cgroup enforcement events recorded in trace.
- Path denylist.
- Process monitor.

### 12.5 Secret appears in output

Mitigation:

- Redact before display/export.
- User-defined secret patterns.
- Raw logs local only.
- Export warnings.

### 12.6 Auto-approval false positive

Mitigation:

- Conservative default policy.
- No model-only auto-approval.
- Unknown prompts require human.
- High-risk action block/ask only.

---

## 13. Testing strategy

## 13.1 Unit tests

- Policy rule matching.
- Redaction.
- Event serialization.
- Adapter command construction.
- Report generation.
- Nucleus launch config generation.
- Sandbox event mapping.

## 13.2 Terminal golden tests

- Raw byte streams through `libghostty` wrapper.
- Screen snapshots compared to expected output.
- Provider prompt fixtures.

## 13.3 Fake CLI integration tests

Create fake provider CLIs that simulate:

- Simple answer.
- Approval prompt.
- TUI-style cursor movement.
- Long-running command.
- File edits.
- Attempted secret read.
- Process hang.

Run the fake CLIs both without a sandbox backend and inside Nucleus strict-agent mode on Linux CI hosts where Nucleus is available.

## 13.4 Real provider smoke tests

Optional tests gated by environment:

```text
RUN_PROVIDER_SMOKE_TESTS=1
```

These should never run in normal CI without explicit credentials.

## 13.5 Security tests

- Malicious OSC sequences.
- Clipboard escape attempts.
- Secret leak fixtures.
- Path traversal.
- Prompt injection in repository files.
- Dangerous command approval prompts.
- Nucleus denies host private path reads.
- Nucleus blocks network in `network: none`.
- Nucleus enforces cgroup PID and memory limits.
- Nucleus PTY console cannot bypass approval injection tracing.

---

## 14. Deployment

## 14.1 Local binary

Initial distribution:

```text
macOS x86_64 / arm64
Linux x86_64 / arm64
```

Later:

```text
Windows x86_64
WASM terminal viewer
CI runner container
```

## 14.2 Build requirements

Potential requirements:

- Zig stable/tagged toolchain for mitos and `libghostty-vt` integration.
- C toolchain only where required by third-party dependencies.
- Pinned Ghostty / `libghostty-vt` source or a compatible prebuilt artifact.
- Platform-specific PTY/ConPTY support.

Distribution should prefer prebuilt binaries so users do not need Zig locally.

Linux distributions should include or locate a compatible Nucleus binary. The mitos release must record the supported Nucleus contract version and fail clearly when the installed Nucleus backend is too old.

---

## 15. Milestones

### Milestone 0: terminal-core proof

- Build `src/terminal/ghostty.zig` integration.
- Feed sample ANSI/VT streams.
- Produce visible text and screen snapshots.
- Golden tests pass.

### Milestone 1: fake provider PTY run

- Spawn fake CLI in PTY.
- Parse output through `libghostty`.
- Detect simple prompt.
- Apply policy.
- Inject response.
- Generate report.
- Run the same fake CLI inside Nucleus strict-agent mode.
- Capture Nucleus sandbox events.

### Milestone 2: real provider headless run

- Implement one headless adapter.
- Launch through Nucleus on Linux.
- Capture output.
- Capture diff.
- Generate report.

### Milestone 3: real provider PTY run

- Implement one PTY provider adapter.
- Connect provider PTY through Nucleus console support on Linux.
- Detect provider approval prompt.
- Human approval flow.
- Raw and redacted logs.

### Milestone 4: policy hardening

- Project config.
- Allow/ask/block rules.
- Secret redaction.
- Process cleanup.
- Timeout handling.

### Milestone 5: viewer

- Terminal replay.
- Approval timeline.
- Diff viewer.
- Final report browser.

---

## 16. Key implementation decisions

### 16.1 Do not fork Ghostty GUI

Use `libghostty-vt` as a terminal core. Implement the product-specific runtime independently.

Reason:

- The product’s differentiator is agent control, not terminal GUI features.
- Forking Ghostty would import unrelated complexity: native windowing, tabs, panes, platform UI, config, rendering.
- `libghostty` exists specifically to let other projects build terminal features without reimplementing the core.

### 16.2 Do not scrape raw ANSI text directly

Always feed PTY output into `libghostty` first. Prompt detection should use normalized screen state and scrollback, not raw escape-coded bytes alone.

### 16.3 Do not make watcher model authoritative

Watcher model may classify or explain, but policy remains deterministic unless the user explicitly grants authority.

### 16.4 Do not depend on provider CLIs as stable APIs

Provider CLIs can change flags, output, and terminal flows. Treat provider adapters as compatibility layers with tests against recorded sessions.

Provider CLIs are external, changing dependencies. Every provider adapter must have version detection, fixtures, and fallback behavior.

### 16.5 Implement mitos in Zig

Use Zig as the primary implementation language. Do not introduce another implementation language solely for `libghostty-vt` or Nucleus integration. Nucleus remains an out-of-process sandbox backend behind the stable launch/event contract.

---

## 17. References

- Ghostty repository: https://github.com/ghostty-org/ghostty
- Ghostty about / `libghostty`: https://ghostty.org/docs/about
- Ghostling minimal `libghostty` terminal: https://github.com/ghostty-org/ghostling
- Ghostty VT documentation: https://ghostty.org/docs/vt
- OpenAI Codex non-interactive mode: https://developers.openai.com/codex/noninteractive
- Claude Code headless mode: https://code.claude.com/docs/en/headless
- Gemini CLI headless mode: https://google-gemini.github.io/gemini-cli/docs/cli/headless.html
- Anthropic Agent SDK provider-boundary note: https://code.claude.com/docs/en/agent-sdk/overview
- OpenAI Terms of Use: https://openai.com/policies/row-terms-of-use/
- Nucleus local spec: `/media/data/proj/nucleus/spec`
- Mitos Nucleus integration contract: `spec/03_NUCLEUS_INTEGRATION.md`
- Mitos Zig implementation plan: `spec/04_IMPLEMENTATION_PLAN.md`
