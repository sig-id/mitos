# PRODUCT.md — Agent Terminal Runtime

Status: draft v0.1  
Date: 2026-05-20  
Working name: **Agent Terminal Runtime**  
Core implementation decision: **use `libghostty` / `libghostty-vt` for terminal emulation and screen-state parsing; do not fork the Ghostty GUI.**
Primary implementation language: **Zig**, with direct `libghostty-vt` integration and an out-of-process Nucleus sandbox backend on Linux.

---

## 1. One-line description

Agent Terminal Runtime is a local-first, provider-neutral runtime that runs AI coding CLIs in controlled sessions, captures what they do, applies approval policies, and produces auditable traces, diffs, transcripts, and run reports.

The product uses provider APIs or headless CLI modes when available, and falls back to supervised PTY/terminal execution using `libghostty` when an AI CLI only exposes an interactive terminal interface.

---

## 2. Product thesis

AI coding CLIs are becoming the default interface for frontier coding agents, but each provider’s CLI has different modes, permissions, prompts, output formats, authentication models, and workflow assumptions.

Developers want to use the best model for each job without rebuilding their workflow around each provider. Teams want repeatable, auditable, policy-controlled agent runs, not opaque terminal sessions.

The winning abstraction is not “another terminal emulator.”

The winning abstraction is:

> A controlled agent-session runtime that can use a terminal as the universal compatibility layer.

`libghostty` gives the product a strong terminal core, but the product’s durable value is in orchestration, sandboxing, approval policy, tracing, provider adapters, and workflow automation.

---

## 3. Problem

### 3.1 Provider CLIs create workflow fragmentation

Many AI providers ship their own coding CLI or terminal agent. These tools are useful, but they often assume the user will work inside that provider’s own harness.

This creates several problems:

- Each provider has a different invocation style.
- Each provider handles permissions and approvals differently.
- Headless / CI mode differs across providers.
- Output formats are inconsistent.
- Interactive terminal flows are difficult to automate safely.
- Users cannot easily compare or swap providers in the same workflow.
- Teams cannot easily audit what the agent saw, changed, approved, or executed.

### 3.2 Terminal transcripts alone are not enough

A terminal transcript shows visible text, but does not reliably tell the whole story.

A production-grade agent runtime also needs:

- Git diffs before and after the run.
- Files created, edited, or deleted.
- Commands executed.
- Approval requests and decisions.
- Process tree metadata.
- Sandbox/network state.
- Redacted terminal transcript.
- Raw terminal bytes for debugging.
- Structured provider events when available.

### 3.3 Existing headless modes are useful but provider-specific

Some provider CLIs already expose non-interactive/headless modes. The product should use those modes first. The PTY/terminal layer should be a compatibility fallback, not the only execution path.

---

## 4. Goals

### 4.1 Primary goals

1. Run AI coding CLIs through a single local runtime.
2. Prefer native API/headless modes when available.
3. Use PTY + `libghostty` terminal emulation when interactive execution is required.
4. Capture complete run evidence: transcript, screen state, diff, commands, approvals, artifacts, final output.
5. Apply user-defined policy to approvals and risky actions.
6. Make interactive AI CLIs usable in controlled automation workflows.
7. Preserve user control: bring-your-own CLI, bring-your-own account, bring-your-own API key.
8. Provide a foundation for local-first agent traces that can later integrate with Gitomi or other Git-native collaboration tools.

### 4.2 Non-goals

This product is not:

- A provider subscription reseller.
- A way to bypass provider rate limits, subscriptions, or safety restrictions.
- A model-training data extractor.
- A full replacement for Ghostty, iTerm, Alacritty, WezTerm, VS Code, or JetBrains.
- A general-purpose RPA system for arbitrary desktop GUIs.
- A guarantee that all provider CLIs can be safely automated.
- A security boundary by itself. The terminal layer is observability and interaction; sandboxing must be implemented separately.

---

## 5. Target users

### 5.1 Solo developer / power user

Wants to use Claude Code, Codex, Gemini CLI, Aider, OpenCode, local agents, and custom scripts without learning a different workflow for each tool.

Main value:

- One command to run different agents.
- Reusable task templates.
- Local transcripts and diffs.
- Ability to compare provider outputs.

### 5.2 Staff engineer / tech lead

Wants AI coding assistance, but needs reviewable evidence before trusting generated changes.

Main value:

- Run report.
- Approval history.
- Test output.
- Git diff capture.
- Safe defaults.

### 5.3 Platform / DevTools engineer

Wants to introduce AI coding agents into team workflows without losing policy control.

Main value:

- Central policy files.
- Local-first audit logs.
- CI-compatible execution.
- Provider adapter model.
- Secrets and network controls.

### 5.4 Open-source maintainer

Wants to run agents on issues, PRs, documentation, and maintenance tasks while preserving a trace of decisions.

Main value:

- Repeatable task runs.
- Publicly shareable sanitized traces.
- Git-native artifacts.
- Agent-generated patch review.

---

## 6. Core user jobs

1. **Run a provider CLI safely**  
   “Run this coding agent on my repository, but keep it inside a controlled workspace.”

2. **Make an interactive agent usable in automation**  
   “When the CLI asks routine questions, apply my policy instead of requiring me to watch the terminal for an hour.”

3. **Review what happened**  
   “Show me what files changed, what commands ran, what approvals were requested, and what the final answer was.”

4. **Compare providers**  
   “Run the same task with Claude, Codex, Gemini, and a local model, then compare diffs and test results.”

5. **Run in CI or batch mode**  
   “Use agent workflows in scripts without opening a TUI.”

6. **Preserve agent context**  
   “Store the useful parts of the agent run as structured local metadata, not just a final patch.”

---

## 7. Product surfaces

### 7.1 CLI

The first product surface should be a CLI.

Example commands:

```bash
atr run --provider claude --task "Fix failing tests"
atr run --provider codex --task-file tasks/review.md
atr run --provider gemini --mode headless --task "Summarize this repo"
atr compare --providers claude,codex,gemini --task "Find security issues"
atr report runs/2026-05-20T18-00-00Z
```

### 7.2 Local run viewer

A local viewer shows:

- Terminal replay.
- Screen-state snapshots rendered through the `libghostty` terminal core.
- Diff viewer.
- Approval timeline.
- Command timeline.
- Final answer.
- Test results.
- Raw and redacted logs.

The first viewer can be simple: static HTML or a local web UI. A native app can come later.

### 7.3 Runtime daemon

A local daemon is optional for MVP but useful later.

Responsibilities:

- Manage active sessions.
- Serve live terminal state.
- Handle approval prompts.
- Stream events to UI clients.
- Persist traces.

### 7.4 Config file

Project-level configuration lives in:

```text
.agent-runtime.yaml
```

Example:

```yaml
version: 1
workspace:
  mode: git-worktree
  base_branch: main
  clean_before_run: true
  container_mount: /workspace

sandbox:
  backend: nucleus
  mode: strict-agent
  runtime: gvisor
  network: none
  workdir: /workspace
  home: /home/agent
  resources:
    memory: 4G
    cpus: 4
    pids: 1024
  toolchain_rootfs: agent-zig-node
  credentials:
    pass_through: explicit_only

providers:
  claude:
    adapter_order: [headless_json, pty]
  codex:
    adapter_order: [exec_json, pty]
  gemini:
    adapter_order: [headless_json, pty]

policy:
  default: ask
  allow:
    - action: read_workspace
    - action: write_workspace
    - action: run_tests
    - command: "git diff*"
    - command: "git status*"
  ask:
    - action: install_package
    - action: network_access
    - action: git_push
    - action: write_outside_workspace
  block:
    - path: "~/.ssh/**"
    - path: "~/.aws/**"
    - path: "~/.config/gh/**"
    - command: "rm -rf /*"

watcher:
  mode: local_only
  model: ollama:qwen3-coder
```

---

## 8. Adapter strategy

The product should use an adapter ladder, from most structured to least structured.

```text
1. Native API / SDK adapter
2. Provider headless CLI adapter
3. Provider interactive CLI over PTY
4. Raw shell / custom CLI adapter
```

The default rule:

> Prefer structured interfaces. Use terminal automation only when structured interfaces are missing or insufficient.

### 8.1 Native API / SDK adapter

Used when the provider exposes stable APIs for messages, tool calls, approvals, costs, and structured events.

Benefits:

- Best semantic fidelity.
- Less terminal parsing.
- Better cost / token accounting.
- Easier automation.

### 8.2 Headless CLI adapter

Used when the provider CLI exposes a non-interactive command with text, JSON, stream JSON, or structured output.

Benefits:

- Usually compatible with provider-authenticated local workflows.
- Easier than PTY scraping.
- Good for CI and scripts.

### 8.3 PTY adapter using `libghostty`

Used when the provider CLI is interactive or when the user explicitly wants the provider’s TUI experience.

Responsibilities:

- Spawn CLI inside a pseudoterminal.
- Feed PTY output into `libghostty-vt`.
- Maintain terminal screen state and scrollback.
- Extract visible text and semantic regions.
- Detect prompts and approval screens.
- Inject user or policy-approved input.
- Record raw terminal bytes and normalized transcript.

---

## 9. Permission and policy model

The runtime must separate “terminal interaction” from “authority.”

A terminal can answer `y`, press Enter, or send text. But the policy engine decides whether the runtime is allowed to do that.

### 9.1 Policy decision types

```text
allow  — runtime may proceed without human approval
ask    — runtime must pause and request approval
block  — runtime must deny or terminate the action
```

### 9.2 Default policy

Default policy should be conservative.

Allowed by default:

- Read files inside workspace.
- Write files inside workspace.
- Run common test commands.
- Run `git status`, `git diff`, `git log`.
- Create local branches / worktrees.

Ask by default:

- Install packages.
- Access network.
- Run unknown shell commands.
- Modify files outside workspace.
- Read hidden config files.
- Push to remote.
- Change global Git config.
- Invoke credential helpers.

Blocked by default:

- Read private key directories.
- Exfiltrate environment variables.
- Modify shell startup files.
- Delete directories outside the workspace.
- Disable sandboxing.
- Bypass provider rate limits or safety restrictions.

### 9.3 Approval UX

Every approval prompt should show:

- Detected action.
- Source: provider prompt, command parser, filesystem observer, or model classifier.
- Raw terminal context.
- Normalized explanation.
- Policy rule matched.
- Suggested decision.
- Risk level.
- Options: allow once, allow for this run, add project rule, deny, terminate.

---

## 10. Run artifacts

Each run should produce a durable artifact directory.

```text
.runs/
  2026-05-20T18-00-00Z_fix-tests_01HXYZ/
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
    screenshots/
    artifacts/
```

### 10.1 `run.yaml`

```yaml
run_id: 01HXYZ
created_at: 2026-05-20T18:00:00Z
task: Fix failing tests
provider: claude
adapter: pty
sandbox:
  backend: nucleus
  mode: strict-agent
  runtime: gvisor
  network: none
workspace: /path/to/repo/.worktrees/01HXYZ
base_commit: abc123
head_commit: def456
status: completed
```

### 10.2 `events.jsonl`

Append-only event log.

Example:

```json
{"type":"RunStarted","run_id":"01HXYZ","timestamp":"2026-05-20T18:00:00Z"}
{"type":"SandboxPrepared","backend":"nucleus","mode":"strict-agent","workspace_mount":"/workspace"}
{"type":"ProcessSpawned","argv":["claude"],"pid":12345}
{"type":"PromptDetected","prompt_kind":"approval","risk":"medium","text":"Run npm test?"}
{"type":"ApprovalDecision","decision":"allow","rule":"allow run_tests"}
{"type":"FileChanged","path":"src/lib.rs","change":"modified"}
{"type":"RunCompleted","status":"completed"}
```

---

## 11. MVP scope

### 11.1 MVP principles

The MVP should prove the core agent-runtime value without trying to become a polished daily-driver terminal.

MVP should prioritize:

- Running agent tasks.
- Capturing reliable traces.
- Applying basic policy.
- Producing useful reports.
- Supporting provider adapters.

MVP should not prioritize:

- Beautiful native terminal UI.
- Advanced panes/tabs/splits.
- Team SaaS dashboard.
- Cross-device sync.
- Full desktop automation.

### 11.2 MVP features

P0:

- Local CLI: `atr run`.
- Provider adapter abstraction.
- At least one headless adapter.
- At least one PTY adapter.
- `libghostty-vt` terminal-state integration.
- Raw and redacted transcript capture.
- Git worktree isolation mounted into a Nucleus `/workspace`.
- Linux sandbox backend using completed Nucleus strict-agent mode.
- Nucleus PTY/console bridge for interactive provider CLIs.
- Nucleus event ingestion into `events.jsonl`.
- Before/after Git diff capture.
- Basic allow/ask/block policy file.
- Approval event log.
- Final Markdown run report.
- Fake-provider test harness.

P1:

- Claude Code adapter.
- Codex CLI adapter.
- Gemini CLI adapter.
- Local model watcher via Ollama-compatible endpoint.
- Terminal replay viewer.
- Secret redaction.
- Command classification.
- Provider version detection.
- Timeout / cancellation / process-tree cleanup.
- Provider toolchain rootfs profiles for common CLI stacks.
- Domain-to-egress policy helper for provider API access.

P2:

- Native desktop UI.
- CI integration.
- Multi-agent comparison runs.
- Team policy templates.
- Gitomi integration.
- Artifact signing.
- Remote runner support.
- macOS and Windows sandbox backends with a Nucleus-compatible contract.

---

## 12. UX flows

### 12.1 Headless run

```bash
atr run --provider codex --task "Explain and fix the failing tests"
```

User sees:

```text
Creating worktree... done
Provider: codex
Adapter: exec/headless
Running task...
Files changed: 3
Tests: passed
Approvals: 0
Report: .runs/01HXYZ/final.md
Patch: .runs/01HXYZ/diff.patch
```

### 12.2 Supervised interactive run

```bash
atr run --provider claude --adapter pty --task "Implement RBAC for admin API"
```

User sees live terminal output. The runtime detects a provider approval prompt:

```text
Approval requested
Action: run command
Command: npm install @casl/ability
Risk: medium
Policy: ask install_package

[Allow once] [Allow for this run] [Deny] [Terminate]
```

### 12.3 Auto-answer routine prompt

Provider asks:

```text
Run cargo test? [Y/n]
```

Policy matches:

```yaml
allow:
  - action: run_tests
```

Runtime sends `y\n`, logs the decision, and continues.

### 12.4 Compare providers

```bash
atr compare \
  --providers claude,codex,gemini \
  --task "Find possible security issues in this auth module"
```

Output:

```text
Provider   Files changed   Tests   Approvals   Status
claude     2               pass    1           completed
codex      1               pass    0           completed
gemini     0               n/a     0           analysis-only
```

---

## 13. Compliance and provider-boundary principles

The product must avoid becoming a provider-access workaround.

Rules:

1. Users bring their own installed provider CLIs or API keys.
2. The product does not resell provider subscriptions.
3. The product does not pool user accounts.
4. The product does not bypass provider rate limits, safety mitigations, or usage restrictions.
5. The product does not hide its automation from the user.
6. The product keeps provider-specific credentials local unless the user explicitly configures a remote runner.
7. The product should prefer official APIs and documented CLI modes when available.

---

## 14. Privacy model

Default posture: local-first.

- Run traces are stored locally.
- Remote watcher models are disabled by default.
- Local watcher models are preferred.
- Secrets are redacted before logs are displayed or exported.
- Environment variables are minimized.
- `.runs/` can be Git-ignored by default.
- The user can explicitly export sanitized traces.

Sensitive values to redact:

- API keys.
- SSH private keys.
- OAuth tokens.
- Database URLs.
- Cookie headers.
- Authorization headers.
- `.env` values.
- Cloud credentials.

---

## 15. Key risks

### 15.1 Terminal parsing fragility

Provider CLIs can change their TUI layout. `libghostty` improves terminal correctness, but does not solve semantic interpretation by itself.

Mitigation:

- Use headless modes first.
- Keep provider-specific prompt detectors.
- Keep raw terminal logs.
- Add fake-CLI regression tests.
- Version adapters by provider CLI version.

### 15.2 `libghostty` API churn

`libghostty-vt` is usable, but its API may evolve.

Mitigation:

- Wrap it behind a narrow internal interface.
- Pin a tested commit.
- Build compatibility tests.
- Avoid leaking `libghostty` API types across the runtime.

### 15.3 False approvals

The most dangerous failure mode is auto-approving a risky action.

Mitigation:

- Conservative default policy.
- No auto-approval for high-risk actions.
- Human approval for unknown commands.
- Process, filesystem, and network observers in addition to terminal parsing.

### 15.4 Provider terms and account risk

Provider terms may restrict third-party use of subscription logins or automated extraction.

Mitigation:

- BYO local CLI only.
- No credential pooling.
- No proxying provider access to other users.
- Prefer API-key mode for third-party/commercial workflows where required.
- Provide clear user-visible compliance warnings.

### 15.5 Terminal is not a sandbox

A terminal controls visible I/O, not the child process’s full authority.

Mitigation:

- Use Nucleus strict-agent mode as the default Linux sandbox backend.
- Use restricted environment variables.
- Use Git worktrees and filesystem boundaries.
- Mount the run worktree into `/workspace`; mount provider credentials explicitly.
- Keep terminal parsing and approval policy in the runtime, not in Nucleus.
- Add non-Linux sandbox backends behind the same sandbox contract.

---

## 16. Success metrics

Activation:

- Time from install to first completed run.
- Percentage of users completing first run report.

Reliability:

- Run completion rate.
- Prompt-detection precision/recall.
- Provider adapter breakage rate after CLI updates.

Safety:

- False auto-approval rate.
- Blocked dangerous-action count.
- Secret-redaction misses.

Value:

- Percentage of runs with useful diffs.
- Percentage of runs where report is opened.
- Repeat usage per repository.
- Provider comparison usage.

Performance:

- PTY output processing latency.
- Terminal snapshot generation latency.
- Run report generation time.

---

## 17. Packaging

Initial distribution:

```text
zig build -Doptimize=ReleaseSafe
brew install agent-terminal-runtime
prebuilt binaries for macOS/Linux
Linux package includes or locates compatible Nucleus backend
```

Later:

```text
native desktop app
VS Code extension
Gitomi integration
CI runner image
```

---

## 18. Open questions

1. Should the first UI be a local web viewer, a Tauri app, or a pure terminal/TUI?
2. Should traces be stored in `.runs/`, `.git/refs/agent/*`, or a Gitomi metadata ref?
3. Should local policy be repository-owned, user-owned, or both?
4. How much provider-specific semantic detection should be hard-coded versus model-classified?
5. Should the watcher model ever be allowed to answer provider prompts automatically without a matching deterministic policy rule?
6. Which non-Linux sandbox backend should follow Nucleus first: macOS Seatbelt, Windows job objects, or a VM/remote runner?
7. Should the product eventually expose an MCP server so agents can write traces directly?
8. Which provider toolchain rootfs profiles should ship first?

---

## 19. References

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
