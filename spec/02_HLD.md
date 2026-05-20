# HLD.md — Agent Terminal Runtime

Status: draft v0.1  
Date: 2026-05-20  
Core terminal dependency: **`libghostty` / `libghostty-vt`**  
Primary implementation language: **Rust**, with a narrow FFI wrapper around `libghostty-vt`.

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

No product component outside `terminal_ghostty` should import raw `libghostty` bindings.

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
│ workspace / env / process /   │    │ fs watcher / git diff /        │
│ network restrictions          │    │ process monitor / redactor     │
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

```rust
trait RuntimeApi {
    fn start_run(&self, request: StartRunRequest) -> Result<RunId>;
    fn subscribe(&self, run_id: RunId) -> EventStream;
    fn approve(&self, approval_id: ApprovalId, decision: ApprovalDecision) -> Result<()>;
    fn cancel_run(&self, run_id: RunId) -> Result<()>;
    fn get_report(&self, run_id: RunId) -> Result<RunReport>;
}
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

```rust
trait AgentAdapter {
    fn provider(&self) -> ProviderId;
    fn capabilities(&self) -> AdapterCapabilities;
    fn start(&mut self, ctx: AdapterContext, task: Task) -> Result<AdapterHandle>;
}

struct AdapterCapabilities {
    supports_headless: bool,
    supports_json_output: bool,
    supports_streaming_events: bool,
    supports_interactive_pty: bool,
    supports_cost_reporting: bool,
    supports_model_selection: bool,
}
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

Create an internal crate:

```text
crates/terminal_ghostty
```

This crate is the only code that links to `libghostty-vt`.

Public Rust-facing interface:

```rust
pub trait TerminalCore {
    fn write_from_pty(&mut self, bytes: &[u8]) -> Result<TerminalEffects>;
    fn resize(&mut self, cols: u16, rows: u16, cell_px: CellSize) -> Result<()>;
    fn snapshot(&self) -> ScreenSnapshot;
    fn visible_text(&self) -> String;
    fn scrollback_text(&self, max_lines: usize) -> String;
    fn encode_key(&self, key: KeyInput) -> Result<Vec<u8>>;
    fn encode_paste(&self, paste: &str) -> Result<Vec<u8>>;
}
```

`libghostty` types must not leak beyond this crate.

### 4.7.3 Screen snapshot model

```rust
pub struct ScreenSnapshot {
    pub cols: u16,
    pub rows: u16,
    pub cursor: CursorState,
    pub title: Option<String>,
    pub alternate_screen: bool,
    pub cells: Vec<Cell>,
    pub timestamp: DateTime<Utc>,
}

pub struct Cell {
    pub x: u16,
    pub y: u16,
    pub text: String,
    pub style: CellStyle,
}

pub struct CellStyle {
    pub bold: bool,
    pub italic: bool,
    pub underline: bool,
    pub inverse: bool,
    pub fg: Option<Color>,
    pub bg: Option<Color>,
}
```

### 4.7.4 Terminal effects

When processing PTY bytes, the terminal core may emit effects:

```rust
pub enum TerminalEffect {
    Bell,
    TitleChanged(String),
    WriteToPty(Vec<u8>),
    ClipboardRequest,
    ClipboardSet { data: Vec<u8> },
    ScreenChanged,
}
```

Security-sensitive effects, especially clipboard and paste effects, must be routed through policy.

### 4.7.5 Build strategy

Recommended initial approach:

```text
third_party/ghostty/       pinned Git submodule or vendored source
crates/terminal_ghostty/   Rust wrapper
build.rs                   builds or links libghostty-vt
bindings.rs                generated by bindgen or handwritten minimal FFI
```

Guidelines:

- Pin a known working Ghostty commit.
- Keep a compatibility matrix in `crates/terminal_ghostty/COMPAT.md`.
- Prefer a small handwritten FFI surface if bindgen produces unstable/noisy bindings.
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

```rust
pub enum PromptEvent {
    ApprovalRequested(ApprovalRequest),
    QuestionAsked(ContextQuestion),
    FinalAnswerCandidate(String),
    IdleDetected,
    ErrorDetected(String),
}
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

```rust
pub trait PolicyEngine {
    fn evaluate(&self, action: Action, context: PolicyContext) -> PolicyDecision;
}

pub enum PolicyDecision {
    Allow { rule_id: RuleId },
    Ask { rule_id: Option<RuleId>, reason: String },
    Block { rule_id: Option<RuleId>, reason: String },
}
```

Action categories:

```rust
pub enum Action {
    ReadPath(PathBuf),
    WritePath(PathBuf),
    DeletePath(PathBuf),
    RunCommand { argv: Vec<String>, cwd: PathBuf },
    NetworkAccess { destination: Option<String> },
    InstallPackage { manager: String, package: String },
    GitPush { remote: String, branch: String },
    ReadSecret { path_or_env: String },
    TerminalInput { bytes: Vec<u8>, reason: String },
}
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

```rust
pub struct ApprovalRequest {
    pub id: ApprovalId,
    pub run_id: RunId,
    pub action: Action,
    pub source: ApprovalSource,
    pub risk: RiskLevel,
    pub terminal_context: Option<ScreenSnapshot>,
    pub matched_rule: Option<RuleId>,
    pub suggested_decision: Option<PolicyDecision>,
}
```

---

## 4.11 Sandbox Layer

The sandbox protects the user’s machine from the child process.

Stage 1 MVP sandbox:

- Git worktree per run.
- Clean environment variable allowlist.
- Working directory locked to workspace.
- Pre-run and post-run Git diff.
- Process-tree cleanup.
- Configurable network warning, not full enforcement.

Stage 2 sandbox:

- Linux: Bubblewrap / namespaces / cgroups where available.
- macOS: Seatbelt-style sandbox profile where available.
- Windows: Job objects + restricted token, or WSL/container backend.
- File allowlist / denylist.
- Optional network block.

Stage 3 sandbox:

- Container runner.
- VM runner for high-risk tasks.
- Remote ephemeral runner.
- Deterministic cache mounts.

The sandbox layer must be independent of `libghostty`.

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
    final.md
    diff.patch
    files_changed.json
    commands.jsonl
    approvals.jsonl
    test_results.jsonl
    artifacts/
```

### 4.13.1 Event schema

```rust
pub enum RuntimeEvent {
    RunStarted(RunStarted),
    WorkspaceCreated(WorkspaceCreated),
    AdapterSelected(AdapterSelected),
    ProcessSpawned(ProcessSpawned),
    PtyBytes(PtyBytesMeta),
    ScreenUpdated(ScreenUpdated),
    PromptDetected(PromptDetected),
    ApprovalRequested(ApprovalRequested),
    ApprovalResolved(ApprovalResolved),
    FileChanged(FileChanged),
    CommandObserved(CommandObserved),
    GitDiffCaptured(GitDiffCaptured),
    HeadlessOutputReceived(HeadlessOutputReceived),
    FinalAnswerDetected(FinalAnswerDetected),
    RunCompleted(RunCompleted),
    RunFailed(RunFailed),
}
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
  → Adapter Registry selects headless adapter
  → Headless Adapter spawns provider command
  → stdout/stderr parsed into events
  → Observability captures diff/files/tests
  → Report Generator writes final.md
  → CLI prints summary
```

Sequence:

```text
CLI → SessionManager: start_run(task, provider)
SessionManager → WorkspaceManager: create_worktree()
SessionManager → AdapterRegistry: select(provider, headless preferred)
Adapter → ProcessRunner: spawn(argv, cwd, env)
ProcessRunner → Adapter: stdout/stderr chunks
Adapter → TraceStore: HeadlessOutputReceived events
SessionManager → GitObserver: capture_diff()
SessionManager → ReportGenerator: finalize()
```

---

## 5.2 PTY execution flow

```text
User runs atr with PTY adapter
  → Session Manager creates workspace
  → PTY Runner opens PTY
  → Provider CLI starts in PTY
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

## 6. Runtime crate layout

Recommended Rust workspace:

```text
agent-terminal-runtime/
  Cargo.toml
  crates/
    atr_cli/                 # CLI binary
    runtime/                 # session manager + runtime API
    adapters/                # provider adapter traits + registry
    adapters_claude/         # provider-specific adapter
    adapters_codex/
    adapters_gemini/
    pty_runner/              # Unix PTY + Windows ConPTY abstraction
    terminal_core/           # terminal trait and shared types
    terminal_ghostty/        # libghostty-vt FFI wrapper
    policy/                  # rules, actions, policy engine
    approvals/               # approval controller
    workspace/               # git worktree manager
    sandbox/                 # sandbox abstraction
    observability/           # fs/process/git observers
    trace_store/             # event log + artifact writer
    redaction/               # secret redaction
    report/                  # final report generator
    watcher/                 # optional local/remote model classifier
    test_fixtures/           # fake CLIs, golden terminal streams
  third_party/
    ghostty/                 # pinned source or submodule
```

---

## 7. `libghostty` FFI design

## 7.1 Binding strategy

Two possible approaches:

### Option A: generated bindings

Use `bindgen` over the C headers.

Pros:

- Faster initial integration.
- Less manual FFI code.

Cons:

- Bindings may be noisy.
- API churn creates large diffs.
- Harder to maintain a clean Rust abstraction.

### Option B: handwritten minimal FFI

Write only the small C function set required by `TerminalCore`.

Pros:

- Stable Rust-facing API.
- Easier review.
- Smaller unsafe surface.

Cons:

- Slower initial integration.
- Requires manual updates when C API changes.

Recommendation:

> Start with handwritten minimal FFI if the required API surface is small; otherwise use bindgen internally but expose only clean safe wrappers.

---

## 7.2 Safe wrapper contract

`terminal_ghostty` should expose safe Rust methods and keep unsafe code local.

```rust
pub struct GhosttyTerminal {
    raw: NonNull<ffi::ghostty_terminal_t>,
    cols: u16,
    rows: u16,
}

impl TerminalCore for GhosttyTerminal {
    fn write_from_pty(&mut self, bytes: &[u8]) -> Result<TerminalEffects> {
        // unsafe FFI call here only
    }

    fn snapshot(&self) -> ScreenSnapshot {
        // convert libghostty cell/state into owned Rust types
    }
}

impl Drop for GhosttyTerminal {
    fn drop(&mut self) {
        // free libghostty terminal
    }
}
```

Safety rules:

- All FFI handles are owned by safe wrapper structs.
- No borrowed data from `libghostty` escapes beyond wrapper call lifetime.
- All snapshots are converted into owned Rust data.
- All functions that can fail map into `Result<TerminalError>`.
- FFI calls are fuzzed/golden-tested with terminal byte streams.

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

```rust
pub trait AgentAdapter: Send {
    fn id(&self) -> AdapterId;
    fn provider(&self) -> ProviderId;
    fn detect(&self) -> Result<AdapterDetection>;
    fn capabilities(&self) -> AdapterCapabilities;
    fn start(&mut self, ctx: AdapterContext, task: Task) -> Result<Box<dyn AdapterRun>>;
}

pub trait AdapterRun {
    fn event_stream(&mut self) -> Pin<Box<dyn Stream<Item = RuntimeEvent> + Send>>;
    fn send_input(&mut self, input: AdapterInput) -> Result<()>;
    fn cancel(&mut self) -> Result<()>;
}
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

```rust
pub struct AdapterDetection {
    pub available: bool,
    pub binary: Option<PathBuf>,
    pub version: Option<String>,
    pub auth_status: AuthStatus,
    pub warnings: Vec<String>,
}
```

## 8.3 Provider-specific prompt detectors

Each provider adapter may register prompt detectors.

```rust
pub trait ProviderPromptDetector {
    fn detect(&self, snapshot: &ScreenSnapshot, context: &PromptContext) -> Vec<PromptEvent>;
}
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

Provider CLIs may need credentials. The runtime should support explicit credential pass-through per provider rather than inheriting all environment variables.

## 9.3 File access

MVP:

- Denylist secret paths in policy.
- Detect suspicious access where observable.
- Use Git diff for output changes.

Later:

- Enforced filesystem sandbox.
- Read-only mounts.
- Writeable workspace only.
- Separate sandbox home.

---

## 10. Security boundaries

## 10.1 Trusted components

Trusted:

- Runtime binary.
- Policy engine.
- Trace store.
- Sandbox setup.
- `terminal_ghostty` wrapper.

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
- OS sandbox in later phase.
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

- Rust stable.
- Zig toolchain for building `libghostty-vt` from source.
- C toolchain.
- Platform-specific PTY/ConPTY support.

Distribution should prefer prebuilt binaries so users do not need Zig locally.

---

## 15. Milestones

### Milestone 0: terminal-core proof

- Build `terminal_ghostty` wrapper.
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

### Milestone 2: real provider headless run

- Implement one headless adapter.
- Capture output.
- Capture diff.
- Generate report.

### Milestone 3: real provider PTY run

- Implement one PTY provider adapter.
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

Provider CLIs are external, changing dependencies. Every provider adapter must have version detection, fixtures, and fallback behavior.

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
