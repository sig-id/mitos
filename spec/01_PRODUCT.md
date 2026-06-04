# PRODUCT.md — Mitos: AI-Native Agent Operating System

Status: draft v0.2
Date: 2026-06-04
Name: **mitos** (earlier working name: "Agent Terminal Runtime")

What it is: **an AI-native agent operating system** — a deterministic runtime
that runs coding agents as controlled, sandboxed, auditable *workers*, while the
runtime itself keeps authority over state, policy, tools, memory, and artifacts.
See `00_ARCHITECTURE.md` for the constitution and `05_LAYERING.md` for the
internal control-plane / engine seam.

Implementation shape: a deterministic **control plane (orchestrator)** on
BEAM/Elixir; an **agent engine** in Zig that owns pluggable transports
(stdio-json, ACP, MCP, cloud-job, http-stream, and a `libghostty-vt` terminal
codec); enforcement via **Nucleus** (gVisor strict-agent) on Linux. The terminal
is *one transport codec, not the spine.*

---

## 1. One-line description

Mitos is a local-first, provider-neutral AI-native operating system: it runs AI
coding agents inside a deterministic runtime that owns the run lifecycle, applies
approval policy, grants scoped capabilities, captures complete evidence, and
produces auditable traces, diffs, transcripts, and run reports.

The runtime prefers provider APIs and headless CLI modes; it falls back to
supervised PTY/terminal execution (`libghostty`) only as one transport among
many, for agents that expose nothing better.

---

## 2. Product thesis

AI coding agents are becoming the default interface for frontier coding work, but
each provider ships a different CLI with different modes, permissions, prompts,
output formats, authentication, and workflow assumptions. Meanwhile, most "agent
frameworks" are a prompt wrapped in a loop: they let the model drive, and hope it
stays inside the lines.

Both problems have the same answer. The winning abstraction is not *another
terminal*, and it is not *a smarter prompt loop*. It is:

> A deterministic AI-native runtime that delegates ambiguous work to stochastic
> agents while retaining authority over state, tools, effects, and audit.

The agent is the stochastic executor. **Mitos is the operating system.**

### 2.1 The product as a layered runtime

At a product level, mitos is six cooperating layers (full architecture in
`00_ARCHITECTURE.md`):

| Layer | What the user gets |
|---|---|
| **Entry surfaces** | One way to launch and review runs from CLI, web viewer, API, CI, or MCP — all hitting the same backend. |
| **Orchestrator** | The control plane: a single, durable run lifecycle; policy decisions; approvals; scheduling. |
| **Agent engine** | Pluggable transports to any provider/agent; structured-first, PTY as compatibility. |
| **Tools & authority** | Typed, capability-scoped tool calls the agent makes *back* into the runtime — not screen-scraped. |
| **Memory** | Runtime-owned context the agent consumes and proposes updates to (target; see §11/§15). |
| **Artifacts & event log** | Durable diffs, reports, transcripts, and an append-only audit trail for every run. |

`libghostty` gives mitos a strong terminal codec, but the durable value is the
orchestration, sandboxing, approval policy, authority plane, tracing, and
transport abstraction around it.

---

## 3. Problem

### 3.1 Provider CLIs create workflow fragmentation

Each provider ships its own coding CLI assuming you work inside its harness:
different invocation styles, permission models, headless modes, output formats,
and auth. Users cannot easily compare or swap providers in one workflow, and
teams cannot audit what an agent saw, changed, approved, or executed.

### 3.2 Terminal transcripts alone are not enough

A transcript shows visible text but not the whole story. A production runtime
also needs git diffs before/after, files touched, commands run, approval requests
and decisions, process-tree metadata, sandbox/network state, redacted and raw
transcripts, and structured provider events when available.

### 3.3 Existing headless modes are useful but provider-specific

Some provider CLIs expose non-interactive modes. Mitos should use those first;
the PTY/terminal codec is a compatibility fallback, not the primary path.

### 3.4 Agent frameworks are prompt-driven, not state-driven

The deeper problem: most agent runtimes let the model implicitly own state,
authority, and control flow. When the process crashes after the agent emits an
effect but before it commits, they cannot recover without guessing. Mitos draws a
hard determinism boundary (`00_ARCHITECTURE.md` §5): the agent proposes; the
runtime decides what is allowed, records what happened, and advances durable
state.

---

## 4. Goals

### 4.1 Primary goals

1. Own the run lifecycle in a single deterministic control plane.
2. Run any provider's agent through pluggable transports; prefer structured
   APIs/headless modes; use PTY + `libghostty` only when required.
3. Capture complete run evidence: transcript, screen state, diff, commands,
   approvals, tool calls, artifacts, final output, event log.
4. Apply user-defined policy to approvals and risky actions, with enforcement
   split across launch (sandbox), synchronous invariants (engine), and
   discretionary decisions (orchestrator).
5. Give agents a typed, capability-scoped **authority plane** to call tools and
   request approvals — not screen-scraped key presses.
6. Make interactive AI CLIs usable in controlled automation.
7. Preserve user control: bring-your-own CLI, account, and API key; local-first.
8. Provide durable run state for cancel/resume and, later, cloud-job reattach.
9. Lay the foundation for runtime-owned memory and Git-native artifacts (Gitomi
   integration later).

### 4.2 Non-goals

Mitos is not:

- A provider subscription reseller or a way to bypass rate limits / safety.
- A prompt-wrapper agent loop without durable state or an authority boundary.
- *Just* a terminal emulator, nor a replacement for Ghostty, iTerm, WezTerm,
  VS Code, or JetBrains.
- A model-training data extractor.
- A general-purpose RPA system for arbitrary desktop GUIs.
- A security boundary *by itself* — the terminal observes; **Nucleus enforces**.

---

## 5. Target users

### 5.1 Solo developer / power user
Wants to use Claude Code, Codex, Gemini CLI, Aider, OpenCode, local agents, and
custom scripts without learning a different workflow per tool. Value: one command
to run any agent, reusable task templates, local transcripts and diffs, provider
comparison.

### 5.2 Staff engineer / tech lead
Wants AI assistance but needs reviewable evidence before trusting changes. Value:
run report, approval history, test output, diff capture, safe defaults.

### 5.3 Platform / DevTools engineer
Wants to introduce agents into team workflows without losing policy control.
Value: central policy files, local-first audit logs, CI-compatible execution,
transport/provider abstraction, secrets and network controls.

### 5.4 Open-source maintainer
Wants to run agents on issues, PRs, docs, and maintenance while preserving a
decision trace. Value: repeatable runs, shareable sanitized traces, Git-native
artifacts, agent-generated patch review.

---

## 6. Core user jobs

1. **Run a provider agent safely** — "Run this agent on my repo, but keep it in a
   controlled, sandboxed workspace."
2. **Make an interactive agent usable in automation** — "Apply my policy to
   routine prompts instead of making me babysit the terminal."
3. **Let the agent call tools under capability** — "When the agent needs to write
   an artifact or request an approval, route it through a typed channel I can
   audit, not a guessed key press."
4. **Review what happened** — "Show me files changed, commands run, approvals
   requested, tools called, and the final answer."
5. **Compare providers** — "Run the same task with Claude, Codex, Gemini, and a
   local model; compare diffs and tests."
6. **Run in CI or batch** — "Use agent workflows in scripts without a TUI."
7. **Resume a run** — "Kill it mid-run and pick up from the last checkpoint."
8. **Preserve agent context** — "Store the useful parts as structured local
   metadata, not just a final patch."

---

## 7. Product surfaces

### 7.1 CLI

The first surface is a CLI. It can run the orchestrator embedded (one-shot/CI) or
talk to a running daemon.

```bash
atr run --provider claude --task "Fix failing tests"
atr run --provider codex --task-file tasks/review.md
atr run --provider gemini --transport headless-json --task "Summarize this repo"
atr compare --providers claude,codex,gemini --task "Find security issues"
atr report runs/2026-05-20T18-00-00Z
atr resume <run_id>
```

### 7.2 Local run viewer

Shows terminal replay, screen-state snapshots rendered through the `libghostty`
codec, diff viewer, approval timeline, tool-call timeline, command timeline,
final answer, test results, and raw/redacted logs. The first viewer can be static
HTML or a local web UI; a native app comes later.

### 7.3 Runtime daemon (the orchestrator)

The daemon is the authority-bearing core, not an optional extra. It is the
control plane: it owns the run lifecycle, mints scoped capabilities, runs the
authority plane, serves live run state, handles approvals, streams events to
clients, and writes the canonical trace. The CLI may embed a degenerate
controller for headless/standalone use with no BEAM (see `05_LAYERING.md` §8).

Responsibilities: manage active sessions; serve live state; handle approval
prompts; broker tool calls; stream events; persist traces and durable state.

### 7.4 Config file

Project-level configuration lives in `.agent-runtime.yaml`:

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
  resources: { memory: 4G, cpus: 4, pids: 1024 }
  toolchain_rootfs: agent-zig-node
  credentials: { pass_through: explicit_only }

providers:
  claude:
    transport_order: [native_api, headless-json, pty]
  codex:
    transport_order: [exec-json, pty]
  gemini:
    transport_order: [headless-json, pty]

authority:
  # the runtime's typed callback plane, mounted into the sandbox as a unix socket
  enabled: true
  tools: [artifact_register, approval, tool_broker, memory_fetch, subrun_spawn]

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

## 8. Transport and adapter strategy

The engine selects a transport per provider, from most structured to least. The
orchestrator states a *preference order*; the engine resolves availability and
picks the highest-preference available transport. The orchestrator never names an
implementation (`05_LAYERING.md` §3).

```text
1. Native API / SDK transport
2. Provider headless CLI (structured: json / stream-json)
3. Provider interactive CLI over PTY (libghostty codec)
4. Raw shell / custom CLI (escape hatch)
```

The default rule:

> Prefer structured interfaces. Use terminal automation only when structured
> interfaces are missing or insufficient.

A transport is a composition of three orthogonal axes — *transport* (byte
movement) × *codec* (interpretation) × *lifecycle* (one-shot vs durable-session)
— so adding one is composition, not a new monolith (`05_LAYERING.md` §3.1).

### 8.1 Native API / SDK
Best semantic fidelity, least parsing, best cost/token accounting, easiest
automation. Used when the provider exposes stable APIs for messages, tool calls,
approvals, costs, and structured events.

### 8.2 Headless CLI (structured)
Provider CLI with a non-interactive command emitting text/JSON/stream-JSON.
Usually compatible with provider-authenticated local workflows; good for CI.

### 8.3 PTY via `libghostty`
Used only when the CLI is interactive or the user explicitly wants the provider's
TUI. The engine spawns the CLI in a pseudoterminal, feeds output into
`libghostty-vt`, maintains screen state and scrollback, extracts visible text and
semantic regions, detects prompts/approval screens, injects policy-approved input
(only with an authorization token, §9), and records raw bytes + normalized
transcript.

### 8.4 Callbacks: prompt = affordance, env = authority
For any transport, the agent calls back into the runtime through the authority
plane. The system prompt tells the agent what it *may* do; injected env
(`OD_DAEMON_URL`, `OD_PROJECT_ID`, `OD_RUN_ID`, `OD_TOKEN`) is what actually
authorizes the callback for subprocesses, scripts, retries, and remote workers
(`00_ARCHITECTURE.md` §2.5).

---

## 9. Permission and policy model

Mitos separates *interaction* from *authority*. A transport can answer `y` or
send text, but the policy engine decides whether the runtime is allowed to, and
input injection requires an authorization token the control plane mints.

### 9.1 Decision types

```text
allow  — proceed without human approval
ask    — pause and request approval
block  — deny or terminate
```

### 9.2 Enforcement tiers

Policy is *decided* in the control plane and *enforced* across three tiers
(`05_LAYERING.md` §4):

| Tier | What | Enforced by |
|---|---|---|
| **1 — Launch** | network/egress, fs mounts & rw boundaries, resource limits, env & credential allow/deny | Nucleus, at launch |
| **2 — Synchronous** | input-injection gating, escape/clipboard sanitization, secret redaction, compiled auto-allow patterns | Engine, no round-trip |
| **3 — Decisional** | run_command, install_package, git_push, write_outside_workspace, read_secret → allow/ask/block | Orchestrator, via authorization token |

Two entry points feed the *same* Tier-3 engine: a scraped prompt (PTY codec) and
a typed `tool_broker` call over the authority plane. The typed path is preferred;
screen-scraping is the fallback for dumb CLIs.

### 9.3 Default policy

Conservative by default. **Allow:** read/write inside workspace, common test
commands, `git status|diff|log`, local branches/worktrees. **Ask:** install
packages, network, unknown shell commands, writes outside workspace, hidden
config reads, push, global Git config changes, credential-helper invocation.
**Block:** private-key dirs, env exfiltration, shell-startup edits, deleting
outside the workspace, disabling sandboxing, bypassing provider limits/safety.

### 9.4 Approval UX

Every approval prompt shows: detected action; source (provider prompt, command
parser, filesystem observer, authority-plane tool call, or model classifier); raw
context; normalized explanation; matched policy rule; suggested decision; risk
level; options (allow once, allow for this run, add project rule, deny,
terminate).

---

## 10. Run artifacts and event log

Each run produces a durable artifact directory. The orchestrator is the canonical
trace writer; the engine writes only the high-volume raw spool and emits
`RAW_REF` offsets, with first-pass redaction at the engine boundary
(`05_LAYERING.md` §7).

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
    tools.jsonl
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
transport: pty
codec: vt-screen
sandbox: { backend: nucleus, mode: strict-agent, runtime: gvisor, network: none }
workspace: /path/to/repo/.worktrees/01HXYZ
base_commit: abc123
head_commit: def456
capabilities: [artifact.write, project.read, tests.run]
status: completed
```

### 10.2 `events.jsonl`

Append-only event log, in the engine→control-plane vocabulary
(`05_LAYERING.md` §5):

```json
{"type":"RUN_STARTED","run_id":"01HXYZ","timestamp":"2026-05-20T18:00:00Z"}
{"type":"SANDBOX_EVENT","backend":"nucleus","mode":"strict-agent","workspace_mount":"/workspace"}
{"type":"PROCESS_SPAWNED","argv":["claude"],"pid":12345}
{"type":"PROMPT_DETECTED","prompt_kind":"approval","risk":"medium","text":"Run npm test?"}
{"type":"ACTION_REQUESTED","action_id":"a1","action":"run_tests","source":"tool_broker"}
{"type":"AUTHORIZE","action_id":"a1","rule":"allow run_tests"}
{"type":"FILE_CHANGED","path":"src/lib.rs","change":"modified"}
{"type":"RUN_COMPLETED","status":"completed"}
```

---

## 11. MVP scope

### 11.1 Principles

Prove the core agent-OS value without becoming a polished daily-driver terminal.
Prioritize: running agent tasks, reliable traces, basic policy, useful reports,
and the pluggable-transport contract. Do **not** prioritize: a beautiful native
terminal, advanced panes/tabs, a team SaaS dashboard, cross-device sync, or
desktop automation.

### 11.2 Features

**P0:**

- Local CLI: `atr run`.
- Orchestrator (control plane) owning the single run lifecycle.
- Engine `Transport` contract; refactor the existing PTY path behind it.
- At least one structured transport (e.g. Claude `--print stream-json`).
- At least one PTY transport via `libghostty-vt`.
- Unified typed-event stream regardless of transport.
- Raw and redacted transcript capture.
- Git worktree isolation mounted into a Nucleus `/workspace`.
- Linux sandbox via Nucleus strict-agent; PTY/console bridge; event ingestion.
- Before/after Git diff capture.
- Basic allow/ask/block policy + Tier-1 launch config + Tier-2 fast-path.
- Approval event log; final Markdown run report.
- Fake-provider test harness.

**P1:**

- Claude Code, Codex, Gemini CLI transports.
- Local watcher via Ollama-compatible endpoint.
- Terminal replay viewer; secret redaction; command classification.
- Provider version detection; timeout/cancellation/process-tree cleanup.
- Authority plane (typed `tool_broker`/`approval`) over the sandbox unix socket.
- Durable run state + cancel/resume keyed by `run_id`.
- Provider toolchain rootfs profiles; domain-to-egress policy helper.

**P2:**

- Native desktop UI; CI integration; multi-agent comparison runs.
- Team policy templates; Gitomi integration; artifact signing.
- Cloud-job transport + reattach; runtime-owned memory subsystem.
- macOS/Windows sandbox backends behind the Nucleus-compatible contract.

---

## 12. UX flows

### 12.1 Headless run

```bash
atr run --provider codex --task "Explain and fix the failing tests"
```

```text
Creating worktree... done
Provider: codex   Transport: exec-json (structured)
Running task...
Files changed: 3   Tests: passed   Approvals: 0
Report: .runs/01HXYZ/final.md   Patch: .runs/01HXYZ/diff.patch
```

### 12.2 Supervised interactive run

```bash
atr run --provider claude --transport pty --task "Implement RBAC for admin API"
```

```text
Approval requested
Action: run command   Command: npm install @casl/ability
Risk: medium   Policy: ask install_package
[Allow once] [Allow for this run] [Deny] [Terminate]
```

### 12.3 Auto-answer routine prompt

Provider asks `Run cargo test? [Y/n]`; policy matches `allow: run_tests`; the
orchestrator mints an authorization token; the engine injects `y\n`, logs the
decision, and continues.

### 12.4 Compare providers

```bash
atr compare --providers claude,codex,gemini --task "Find security issues in this auth module"
```

```text
Provider   Files changed   Tests   Approvals   Status
claude     2               pass    1           completed
codex      1               pass    0           completed
gemini     0               n/a     0           analysis-only
```

---

## 13. Compliance and provider-boundary principles

Mitos must not become a provider-access workaround.

1. Users bring their own installed provider CLIs or API keys.
2. No reselling of provider subscriptions.
3. No pooling of user accounts.
4. No bypassing provider rate limits, safety mitigations, or usage restrictions.
5. No hiding automation from the user.
6. Provider credentials stay local unless the user configures a remote runner.
7. Prefer official APIs and documented CLI modes when available.

---

## 14. Privacy model

Default posture: local-first.

- Run traces are stored locally; `.runs/` can be Git-ignored by default.
- Remote watcher models are disabled by default; local watchers preferred.
- Secrets are redacted before logs are displayed or exported; redaction happens
  at the engine boundary so raw secrets never cross the control-plane seam.
- Environment variables are minimized; the user can export sanitized traces.

Redact: API keys, SSH private keys, OAuth tokens, database URLs, cookie headers,
authorization headers, `.env` values, cloud credentials.

---

## 15. Key risks

### 15.1 Terminal parsing fragility

Provider TUIs change layout; `libghostty` improves terminal correctness but not
semantic interpretation. **Primary mitigation is structural:** the authority
plane (`00_ARCHITECTURE.md` §2.5) lets agents make typed `tool_broker`/`approval`
calls, so `ACTION_REQUESTED` originates from a structured channel rather than the
vt-screen codec guessing from pixels. Screen-scraping demotes to the fallback for
dumb CLIs. Additional mitigations: prefer headless transports, keep
provider-specific detectors, keep raw logs, add fake-CLI regression tests,
version detectors by provider CLI version.

### 15.2 `libghostty` API churn

`libghostty-vt` is usable but evolving. Mitigation: wrap it behind a narrow
internal interface (`src/terminal/ghostty.zig`), pin a tested commit, add
compatibility tests, never leak `libghostty` types across the engine.

### 15.3 False approvals

The most dangerous failure is auto-approving a risky action. Mitigation:
conservative defaults; no auto-approval for high-risk actions; human approval for
unknown commands; process/filesystem/network observers in addition to prompt
parsing; input injection gated behind an orchestrator-minted token.

### 15.4 Provider terms and account risk

Provider terms may restrict third-party use of subscription logins or automated
extraction. Mitigation: BYO local CLI only; no credential pooling; no proxying
provider access; prefer API-key mode where required; clear compliance warnings.

### 15.5 Terminal is not a sandbox

A terminal controls visible I/O, not the child's full authority. Mitigation:
Nucleus strict-agent as the default Linux sandbox; restricted env; Git worktrees
and filesystem boundaries; mount the worktree into `/workspace` and credentials
explicitly; keep policy/approval in the runtime, not in Nucleus; add non-Linux
backends behind the same contract.

---

## 16. Success metrics

- **Activation:** time from install to first completed run; share of users
  completing a first run report.
- **Reliability:** run completion rate; prompt-detection precision/recall;
  transport breakage rate after provider CLI updates.
- **Safety:** false auto-approval rate; blocked dangerous-action count;
  secret-redaction misses.
- **Value:** share of runs with useful diffs; share of runs where the report is
  opened; repeat usage per repo; provider-comparison usage.
- **Performance:** transport/PTY processing latency; snapshot generation latency;
  report generation time.

---

## 17. Packaging

Mitos ships as two cooperating artifacts: the BEAM orchestrator and the Zig
engine (plus a located/bundled Nucleus backend on Linux). Standalone packaging of
the BEAM control plane is a known cost and an explicit non-goal for now
(`05_LAYERING.md` §1.2); the Zig engine can run headless with an embedded
controller for CI/standalone.

```text
zig build -Doptimize=ReleaseSafe          # engine
mix release                                # orchestrator
prebuilt binaries for macOS/Linux
Linux package includes or locates a compatible Nucleus backend
```

Later: native desktop app, VS Code extension, Gitomi integration, CI runner
image.

---

## 18. Open questions

1. Should the first UI be a local web viewer, a Tauri app, or a TUI?
2. Should traces live in `.runs/`, `.git/refs/agent/*`, or a Gitomi metadata ref?
3. Should local policy be repo-owned, user-owned, or both?
4. How much provider-specific semantic detection is hard-coded vs
   model-classified?
5. Should the watcher model ever answer provider prompts without a matching
   deterministic policy rule?
6. Which non-Linux sandbox backend follows Nucleus first: macOS Seatbelt, Windows
   job objects, or a VM/remote runner?
7. The authority plane already settles *inbound* MCP (agent → runtime callbacks).
   Open question: should mitos *also* expose an **outbound MCP server** so
   external clients/agents can drive runs and read traces directly?
8. Which provider toolchain rootfs profiles ship first?

---

## 19. References

- `00_ARCHITECTURE.md` — the constitution: mitos = the AI-native OS.
- `02_HLD.md` — component-level high-level design.
- `03_NUCLEUS_INTEGRATION.md` — sandbox launch/event (enforcement) contract.
- `04_IMPLEMENTATION_PLAN.md` — original Zig workstreams.
- `05_LAYERING.md` — the orchestrator ↔ engine seam, policy tiers, control protocol.
- Ghostty: https://github.com/ghostty-org/ghostty · `libghostty`:
  https://ghostty.org/docs/about · VT docs: https://ghostty.org/docs/vt
- Ghostling minimal `libghostty` terminal:
  https://github.com/ghostty-org/ghostling
- OpenAI Codex non-interactive: https://developers.openai.com/codex/noninteractive
- Claude Code headless: https://code.claude.com/docs/en/headless
- Gemini CLI headless:
  https://google-gemini.github.io/gemini-cli/docs/cli/headless.html
- Anthropic Agent SDK: https://code.claude.com/docs/en/agent-sdk/overview
- OpenAI Terms of Use: https://openai.com/policies/row-terms-of-use/
- Nucleus local spec: `/media/data/proj/nucleus/spec`
