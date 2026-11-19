# ADR 0008: Nucleus as the enforcing local execution provider

Status: proposed, 2026-11-19. Evaluated against Nucleus 0.5.3, re-verified
against Nucleus 0.6.0 (`c29a839`), which carries the fixes listed below.
Contracts: [grants, resources and providers](../system/02_RUNTIME.md#grants-resources-and-providers),
[resource policy enforcement](../system/02_RUNTIME.md#resource-policy-scopes-and-enforcement),
[M4](../system/06_DELIVERY_AND_CONFORMANCE.md#m4--enforced-execution-and-verified-continuity).

## Context

M4 requires a provider that enforces declared confinement, limits and revocation.
The local provider runs as the operator. Its optional bubblewrap confinement
hides the host filesystem and network but enforces no CPU, memory or process
limits, reports nothing back, and leaves `agent.cpu_millicores`,
`agent.memory_mib` and `workflow.tokens` without an enforcement point.

[Nucleus](https://github.com/sig-id/nucleus) is a rootless Linux sandbox runtime
with namespaces, cgroups v2, seccomp, Landlock and an optional gVisor runtime.
Its strict-agent mode fails closed instead of degrading. Version 0.5.3 added what
an orchestrator needs to embed it: a launch document on a descriptor
(`--config-fd`), a lifecycle event stream (`--events-fd`) that keeps stdio
clean for the workload, rootless brokered egress (`--broker-socket`), volumes
owned by the caller, cgroups in a delegated subtree, and a reaper that kills
the container when its supervisor dies.

### Observed behavior

Tested rootless on NixOS, Linux 7.3.3, systemd 260, in strict-agent mode. The
0.5.3 column is the original evaluation; the 0.6.0 column is after the fixes,
checked with the Mitos integration tests and direct runs.

| Property | 0.5.3 | 0.6.0 |
| --- | --- | --- |
| Seccomp, Landlock, all capabilities dropped; host home invisible | yes | yes |
| Over-cap workload is killed | no: throttled at `memory.high` | yes, OOM-killed at `memory.max`; `memory_events` in the summary ([nucleus#37](https://github.com/sig-id/nucleus/issues/37)) |
| Workload held until the caller verifies the posture | no: it runs as `container_started` is emitted | yes, with `start_fd`; EOF tears the container down unrun ([nucleus#42](https://github.com/sig-id/nucleus/issues/42)) |
| Brokered egress without a forwarder binary | no: the workload must run its own | yes, `broker_proxy_listen`; the Mitos CONNECT relay works unchanged as the broker ([nucleus#39](https://github.com/sig-id/nucleus/issues/39)) |
| Through the broker: allowlisted model host / other host / direct | reachable / refused / no route | same |
| gVisor with a broker socket | reaches every mounted host socket | refused ([nucleus#36](https://github.com/sig-id/nucleus/issues/36)) |
| Nested user namespace (bubblewrap) | native killed by `SIGSYS`; gVisor refuses the uid map | native fails with `EPERM`; documented as unsupported ([nucleus#35](https://github.com/sig-id/nucleus/issues/35)) |
| Cgroup delegation detected for a user-manager unit | no; `NUCLEUS_CGROUP_ROOT` works | yes ([nucleus#34](https://github.com/sig-id/nucleus/issues/34)) |
| Supervisor SIGKILL kills workload and removes cgroup | yes; gVisor state directory remains | state directory reaped ([nucleus#38](https://github.com/sig-id/nucleus/issues/38)); not re-tested |
| Caller-owned volume is executable | no | no |
| Opt-in syscall not passed in `seccomp_allow` | process killed (`SIGSYS`) | same: Codex dies on `inotify_init1`, `touch` and `git` on `utimensat`, every Node on `io_uring_setup` ([nucleus#45](https://github.com/sig-id/nucleus/issues/45)) |
| JIT runtimes (Node/V8 maps RWX code memory) | not tested | killed by the W^X rule; `--jitless` disables WebAssembly, which Node's HTTP client needs ([nucleus#46](https://github.com/sig-id/nucleus/issues/46)) |

Codex 0.169 always sandboxes its shell commands with a bundled bubblewrap; its
Landlock-only fallback is deprecated. Inside either Nucleus runtime every such
command fails. Claude Code's optional Linux sandbox also uses bubblewrap.

## Decision

Add Nucleus as a separate `compute.Provider`, not as another argv-wrapping
`Confinement` tool. Wrapping argv cannot carry cgroup setup, the event stream
or cgroup-based stop.

**One enforcing layer per attempt.** Under Nucleus the harness runs without its
own sandbox (Codex `--sandbox danger-full-access`, Claude Code sandbox off).
Nucleus enforces the posture Mitos admitted. Admission refuses a harness sandbox
under Nucleus and refuses an unsandboxed harness outside an enforcing provider.
The attempt records which layer enforces. Nested sandboxes are not pursued:
user namespaces are a large kernel attack surface, and Nucleus blocks them
deliberately.

**Mitos decides; Nucleus enforces and attests.** `compute.Compile` freezes the
posture as today. The provider renders it into a Nucleus launch document with a
pure, golden-tested function and launches
`nucleus run --config-fd 3 --events-fd 4` with a start gate in the document.
Nucleus holds the workload while the `container_started` event is compared with
the admitted posture: seccomp, Landlock, capabilities, limits, network mode,
workspace mount and cgroup path. Only a match releases the gate; any mismatch
closes it, so the workload never runs. `container_summary` supplies exit
status, statistics and OOM kills to the journal.

**Capabilities are probed, not declared.** At startup the provider launches a
canary container and derives `Capabilities` from its events: delegated cgroup,
Landlock ABI, broker socket and gVisor availability. CPU and memory caps are
advertised only when the probe confirms a delegated cgroup. The result is cached
per Nucleus version and kernel.

**Lifecycle.**

- *Prepare* materializes the workspace copy as today.
- *Stop* writes `cgroup.kill` at the path from `container_started`.
- *Recovery* after a server restart finds `nucleus-<attempt>` cgroups in the
  server's delegated subtree, kills them and records the outcome as unknown.
- The server runs as a systemd user service with `Delegate=yes` and
  `DelegateSubgroup=`. It names its subtree with `NUCLEUS_CGROUP_ROOT` even
  though Nucleus now detects user-manager delegation: Mitos reclaims that
  subtree at startup, so it must be the one the operator named, not one
  inferred.

**Filesystem.**

| Path | Contents |
| --- | --- |
| `/` | Pinned agent toolchain rootfs (Nix store path recorded in the posture) containing the harness CLIs and git. Until it exists, the host system directories and `/nix/store`, so the harness executable must be a store path |
| `/workspace` | The Mitos workspace copy, mounted read-only or read-write by role; this replaces the harness's read-only and workspace-write modes |
| `/mitos/home` | The per-attempt home, a caller-owned read-write volume holding Mitos-generated harness configuration |
| `/mitos/inputs` | Dependency inputs, read-only |
| `/mitos/context` | Context sections spilled from the prompt, read-only |

No host toolchain is bound. Caller-owned volumes are not executable, so
executables come only from the rootfs.

**Egress and credentials.** The network mode is `none` with `--broker-socket`
and `--broker-proxy-listen 127.0.0.1:3128`: Nucleus listens on loopback inside
the container and relays each connection to a per-attempt host broker over the
socket, which is not mounted. Proxy variables are not injected. The harness is
configured with a plain-HTTP base URL on that listener (Claude Code
`ANTHROPIC_BASE_URL`, Codex `openai_base_url`; Codex ignores `OPENAI_BASE_URL`)
and a placeholder key. The broker is a reverse proxy, not a tunnel. The first path
segment names the vendor API, which is the only upstream it reaches. It removes
every client credential, adds the operator's API key and makes the TLS
connection itself, so the container needs no CA bundle. It refuses `CONNECT`. No
model credential enters the sandbox. This is required rather than optional:
without a harness sandbox, every command the agent runs can read the harness
environment. The broker sees every request and response, which gives
`workflow.tokens` its future enforcement point; counting there is not built yet,
and usage is still reported by the harness.

Pi is brokered for its configured default provider (`zai`, `openai` or
`anthropic`). Its sandbox configuration keeps the operator's model definitions
for that provider, with the base URL pointed at the broker and keys, headers
and other providers removed. Only API keys are brokered. Subscription logins (ChatGPT, claude.ai) would
require Mitos to reimplement each vendor's token refresh, so an agent under
Nucleus without an API key is refused. The broker reads the key from
`NAME`, `NAME_FILE`, or a `NAME` holding an absolute path to a file, as secret
managers provide it, on every attempt.

**System calls.** The strict-agent profile kills a process that makes an
opt-in call it was not given. Agent launches pass a fixed list of benign opt-in
calls through `seccomp_allow`, frozen in the posture: inotify, timestamps,
pidfds, timers, protection keys, priorities and limits, `sysinfo`, and
`splice`/`sendfile`. Without it Codex dies at startup on `inotify_init1`.
io_uring, namespaces, memory policy, identity changes and extended attributes
stay denied. Any call outside the list still kills the caller;
[nucleus#45](https://github.com/sig-id/nucleus/issues/45) proposes an errno
instead.

**Trust tiers.**

| Tier | Placement |
| --- | --- |
| T1 | Native runtime, strict-agent |
| T0 | gVisor runtime, network mode `off` only: Nucleus refuses a broker under gVisor, so T0 agents, which need model egress, are not placed locally |
| T2 | Unchanged (remote microVM) |

Once the Nucleus provider passes M4 conformance, the bubblewrap confinement is
removed rather than kept as a second local provider.

## Consequences

Local execution gains enforced caps, an attested posture, revocation that
survives supervisor and server crashes, and metered model access. M4's
"provider that enforces declared confinement, limits and revocation" becomes
reachable locally.

The harness can no longer separate its own model access from the commands it
runs. Both share the broker's reach, so `network.read` is not presented as
enforced under this provider. The broker allowlist and credential injection
bound the harm.

Nix becomes a requirement for confined local execution, because the toolchain
rootfs is a Nix closure. Hosts without Nix retain only unconfined development
execution.

A workload over its memory cap is OOM-killed; the provider reports it as
`memory_exhausted` from the summary's `memory_events`, distinct from other
failures.

The owning specifications change on acceptance: [runtime](../system/02_RUNTIME.md)
gains the single-enforcing-layer admission rule and the probed capabilities;
[delivery](../system/06_DELIVERY_AND_CONFORMANCE.md) gains the conformance
evidence listed below.

## Delivery

Applications that declare a confined posture are served first: they need no
model egress. Agents follow on the same provider; the Nucleus forwarder removes
the need to ship a forwarder in the rootfs, which caller-owned volumes and
`/context` could not have run. The provider requires Nucleus 0.6.0 or later.

1. Provider: launch document, events, posture comparison, cgroup stop, crash
   recovery, capability probe, delegated user service.
2. Toolchain rootfs with Codex packaged from its static binary, plus Claude
   Code and git; per-attempt home and inputs volumes; path translation for
   `HOME`, `MITOS_WORKSPACE` and `GIT_CEILING_DIRECTORIES`. Volumes and path
   translation are done; the rootfs is not, so the harness executable must
   be a Nix store path. Home-installed npm wrappers cannot run.
3. Harness posture (done): unsandboxed harness under Nucleus
   (`MITOS_AGENT_CONFINEMENT=nucleus`), the workspace mount chosen by role, the
   harness system calls, and a whitelisted environment.
4. Broker: `--broker-socket` routing and credential injection (done);
   per-attempt token accounting (not yet).
5. Conformance, then removal of the bubblewrap confinement. Required evidence:
   cap exhaustion, supervisor SIGKILL, server crash recovery, refusal of a
   degraded start, refusal of a nested sandbox, and no credential visible inside
   the sandbox.

## Alternatives

- **Keep bubblewrap and add cgroups.** This rebuilds Nucleus inside Mitos
  without the fail-closed checks, event attestation or gVisor.
- **Allow nested user namespaces.** This keeps the per-command split but
  reopens the kernel surface Nucleus closes, for one harness feature. Rejected.
- **Credential in the sandbox, restricted by the egress allowlist.** Any command
  the agent runs could read and replay the key. Rejected.
- **Docker or Podman.** These use a daemon or broad defaults, do not deny egress
  by default, and offer no attestation stream. They give less than Nucleus and
  add a service dependency.
