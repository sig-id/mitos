# NUCLEUS_INTEGRATION.md — Linux Sandbox Backend Contract

Status: draft v0.1  
Date: 2026-05-20  
Assumption: required Nucleus-side integration work is complete.

---

## 1. Purpose

Nucleus is the Linux sandbox backend for Agent Terminal Runtime.

Mitos owns:

- Provider adapter selection.
- Provider command construction.
- Approval policy.
- Terminal parsing through `libghostty-vt`.
- Prompt detection.
- Run artifacts and reports.

Nucleus owns:

- Linux isolation.
- Workspace and secret mounts.
- PTY/console attachment.
- cgroup resource limits.
- seccomp, Landlock, capabilities, and gVisor enforcement.
- Network namespace and egress policy enforcement.
- Low-level sandbox lifecycle and audit events.

The integration boundary must keep these responsibilities separate. Nucleus must not parse provider prompts or make mitos approval decisions. Mitos must not treat terminal output as a security boundary.

---

## 2. Backend Selection

Default Linux backend:

```yaml
sandbox:
  backend: nucleus
  mode: strict-agent
  runtime: gvisor
```

Allowed backend modes:

```text
none            no sandbox, development only
host-process    worktree + env restrictions, no kernel isolation
nucleus         Linux strict-agent sandbox
remote          future remote ephemeral runner
vm              future high-risk local VM runner
```

`nucleus` is the only backend that satisfies the Linux public-release sandbox requirement.

---

## 3. Launch Contract

Mitos launches a provider process through Nucleus using an out-of-process launch contract: CLI invocation, config file / config FD, or another machine-readable transport. The Zig runtime must not depend on Nucleus internals. The exact transport is not product-visible, but the semantic contract is stable.

Required launch fields:

```yaml
run_id: 01HXYZ
container_name: atr-01HXYZ
mode: strict-agent
runtime: gvisor

command:
  argv: ["claude", "--some-provider-flag"]
  cwd: /workspace

mounts:
  workspace:
    host_path: /repo/.agent-worktrees/01HXYZ
    container_path: /workspace
    access: read_write_execute
  home:
    container_path: /home/agent
    type: tmpfs
  context:
    host_path: /repo
    container_path: /context
    access: read_only
    optional: true

environment:
  HOME: /home/agent
  WORKSPACE: /workspace
  TERM: xterm-256color
  LANG: C.UTF-8

resources:
  memory: 4G
  cpus: 4
  pids: 1024

network:
  mode: none

events:
  format: jsonl
  channel: sidecar
```

Nucleus must return:

```zig
pub const SandboxHandle = struct {
    run_id: RunId,
    backend: BackendId = .nucleus,
    container_id: []const u8,
    process_id: ?u32,
    workspace_mount: []const u8, // /workspace
    event_stream: SandboxEventStream,
    io: SandboxIo,
};
```

---

## 4. Path Contract

Nucleus exposes these paths inside the container:

```text
/workspace       writable run worktree
/context         optional read-only supplemental context
/home/agent      sandbox home
/run/secrets     explicit provider credentials
/tmp             writable scratch
```

Rules:

- Provider commands always start in `/workspace`.
- Mitos computes diffs from the host worktree path, not from container internals.
- Host repository root is not mounted except through the explicit `/workspace` or optional `/context` mounts.
- Host credential directories are not mounted by default.
- `/workspace` may need execute permission for build and test workflows.
- `/tmp` and `/home/agent` must not permit executing newly written files unless explicitly configured by the selected toolchain profile.

---

## 5. PTY Contract

PTY mode is required for interactive provider CLIs.

Mitos-side responsibilities:

- Own the terminal core.
- Feed all PTY bytes into `libghostty-vt`.
- Record raw bytes in `terminal.raw`.
- Detect prompts from screen snapshots.
- Inject only approved input.

Nucleus-side responsibilities:

- Create or attach to the provider process PTY.
- Keep the provider process inside the Nucleus sandbox.
- Propagate terminal resize events.
- Forward signals according to mitos cancellation policy.
- Keep Nucleus audit events separate from provider PTY bytes.

PTY data flow:

```text
provider process inside Nucleus
  → Nucleus PTY/console
  → mitos PTY runner
  → terminal.raw
  → libghostty-vt
  → PromptDetector
```

Mitos must treat every injected byte as a policy-controlled action.

---

## 6. Headless Contract

For provider headless/exec modes, Nucleus exposes stdout, stderr, exit status, and sandbox events separately.

```text
stdout/stderr     provider output
sandbox events    Nucleus lifecycle/audit/resource stream
exit status       provider process result
```

Mitos adapters parse provider stdout/stderr. They must not parse Nucleus control logs from the same stream.

---

## 7. Credential Contract

Provider credentials are explicit.

Allowed mechanisms:

- Secret files mounted under `/run/secrets`.
- Provider-specific config directories mounted into `/home/agent` when explicitly configured.
- Environment variables only when a provider requires them and mitos policy allows them.

Disallowed by default:

- Inheriting all host environment variables.
- Mounting host `~/.ssh`, `~/.aws`, `~/.config/gh`, cloud credential directories, or shell startup files.
- Passing secrets through argv.
- Sending secrets to watcher models.

Provider adapters are responsible for mapping secret files into the provider-specific authentication mechanism.

---

## 8. Policy Translation

Mitos policy has semantic actions. Nucleus policy has kernel-level enforcement. Mitos translates between them before launch.

Examples:

```text
Action::ReadPath(/workspace/**)
  → Nucleus /workspace read permission

Action::WritePath(/workspace/**)
  → Nucleus /workspace write permission

Action::RunCommand(cargo test)
  → no Nucleus policy change if /workspace execution is already enabled

Action::NetworkAccess(api.anthropic.com:443)
  → Nucleus network mode + egress allowlist

Action::ReadSecret(provider_token)
  → Nucleus secret mount under /run/secrets/provider_token
```

Runtime prompt approval cannot widen Nucleus kernel policy in-place. If a run needs broader filesystem or network authority, mitos must pause and either:

- deny the action,
- ask the user to restart with a broader sandbox profile,
- or start a new sandbox with explicit updated policy.

---

## 9. Event Contract

Nucleus emits JSONL events. Mitos maps them into `RuntimeEvent`.

Required Nucleus event classes:

```text
container_start
container_stop
namespace_created
cgroup_created
filesystem_mounted
mount_audit_passed
capabilities_dropped
seccomp_applied
seccomp_profile_loaded
landlock_applied
no_new_privs_set
network_bridge_setup
egress_policy_applied
egress_denied
secrets_mounted
resource_sample
```

Mitos mapping:

```text
container_start        → SandboxPrepared
cgroup_created         → SandboxEvent
filesystem_mounted     → SandboxEvent
capabilities_dropped   → SandboxEvent
seccomp_applied        → SandboxEvent
landlock_applied       → SandboxEvent
egress_policy_applied  → SandboxEvent
egress_denied          → SandboxEvent
resource_sample        → ResourceSample
container_stop         → SandboxEvent
```

Nucleus event payloads must include:

```json
{
  "timestamp": "2026-05-20T18:00:01.123Z",
  "run_id": "01HXYZ",
  "container_id": "nucleus-id",
  "event_type": "seccomp_applied",
  "detail": "...",
  "is_error": false
}
```

Mitos stores the original stream in:

```text
sandbox.events.jsonl
```

Mitos stores normalized resource samples in:

```text
resources.jsonl
```

---

## 10. Toolchain Profiles

Provider CLIs and language toolchains must be available inside the sandbox.

Mitos refers to toolchains by profile name:

```yaml
sandbox:
  toolchain_rootfs: agent-zig-node
```

Each profile defines:

- Provider CLI binaries.
- `git`.
- Shell.
- Language runtimes.
- Package managers.
- CA certificates.
- Minimal system files required by the provider CLI.

Profiles should be reproducible and versioned. A run report must record the selected profile and digest.

---

## 11. Failure Semantics

Nucleus startup failure is a run preparation failure.

Fail closed on:

- Failed seccomp enforcement in strict-agent mode.
- Failed Landlock enforcement in strict-agent mode.
- Failed cgroup creation when resource limits are requested.
- Failed workspace mount.
- Failed secret mount.
- Failed PTY/console setup for PTY adapters.
- Requested network policy that Nucleus cannot enforce.

Mitos may fall back to host-process mode only when the user explicitly requests or approves a degraded run.

---

## 12. Reporting

Run reports should include:

- Sandbox backend.
- Nucleus runtime: native or gVisor.
- Effective workspace path.
- Effective network policy.
- Resource limits.
- Security enforcement state.
- Any degraded or failed Nucleus enforcement.
- Link/path to `sandbox.events.jsonl`.
- Resource peak summary from `resources.jsonl`.

Example:

```text
Sandbox: nucleus strict-agent, gVisor
Workspace: /workspace → /repo/.agent-worktrees/01HXYZ
Network: none
Resources: memory=4G cpus=4 pids=1024
Security: seccomp=applied, landlock=applied, capabilities=dropped
```

---

## 13. Non-Linux Backends

Non-Linux backends must expose the same mitos-facing contract:

- A workspace mount or equivalent.
- A sandbox home.
- Explicit credential pass-through.
- Isolated process execution.
- PTY and headless I/O.
- Machine-readable sandbox events.
- Clear degraded-mode reporting.

They do not need to implement Nucleus internals. They must report weaker guarantees honestly.
