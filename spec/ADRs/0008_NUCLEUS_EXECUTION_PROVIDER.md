# ADR 0008: Nucleus as the enforcing local execution provider

Status: proposed, 2026-11-19. Evaluated against Nucleus 0.5.3; implementation pending.
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

Tested rootless on NixOS, Linux 7.3.3, systemd 260, with Nucleus 0.5.3 in
strict-agent mode:

| Property | Native | gVisor |
| --- | --- | --- |
| Seccomp, Landlock, all capabilities dropped; host home invisible | yes | gVisor kernel |
| Memory cap contains the workload | yes | yes |
| Over-cap workload is killed | no: throttled at `memory.high` ([nucleus#37](https://github.com/sig-id/nucleus/issues/37)) | no, same |
| Broker socket reachable; other TCP unreachable; loopback usable | yes | yes |
| Other mounted host sockets unreachable | yes (Landlock) | no ([nucleus#36](https://github.com/sig-id/nucleus/issues/36)) |
| Supervisor SIGKILL kills workload and removes cgroup | yes | yes; state directory remains ([nucleus#38](https://github.com/sig-id/nucleus/issues/38)) |
| Nested user namespace (bubblewrap) | killed by seccomp ([nucleus#35](https://github.com/sig-id/nucleus/issues/35)) | refused at uid map |
| Cgroup delegation detected for a user-manager unit | no; `NUCLEUS_CGROUP_ROOT` works ([nucleus#34](https://github.com/sig-id/nucleus/issues/34)) | same |
| Caller-owned volume is executable | no | not tested |

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
`nucleus run --config-fd 3 --events-fd 4`. The `container_started` event is
compared with the admitted posture: seccomp, Landlock, capabilities, limits,
network mode and cgroup path. Any mismatch kills the container and refuses the
attempt. `container_summary` supplies exit status and statistics to the journal.

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
  `DelegateSubgroup=`. It sets `NUCLEUS_CGROUP_ROOT` explicitly until
  nucleus#34 is resolved.

**Filesystem.**

| Path | Contents |
| --- | --- |
| `/` | Pinned agent toolchain rootfs (Nix store path recorded in the posture) containing the harness CLIs, git and the egress forwarder |
| `/workspace` | The Mitos workspace copy, mounted read-only or read-write by role; this replaces the harness's read-only and workspace-write modes |
| `/home/agent` | The per-attempt home, a caller-owned read-write volume holding Mitos-generated harness configuration |
| `/inputs` | Dependency inputs, read-only |

No host toolchain is bound. Caller-owned volumes are not executable, so
executables come only from the rootfs.

**Egress and credentials.** The network mode is `none` with `--broker-socket`.
An in-container forwarder on `127.0.0.1:3128` reaches a per-attempt host broker
over the socket. The broker allowlists model endpoints, adds the credential,
makes the TLS connection upstream and counts tokens per attempt. No model
credential enters the sandbox. This is required rather than optional: without
a harness sandbox, every command the agent runs can read the harness
environment. Counting at the broker gives `workflow.tokens` an enforcement
point.

**Trust tiers.**

| Tier | Placement |
| --- | --- |
| T1 | Native runtime, strict-agent |
| T0 | gVisor runtime, admitted only after nucleus#36 is resolved and the single-socket broker boundary holds |
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

Until nucleus#37 is resolved, a workload over its memory cap stalls until the
attempt deadline instead of failing. The provider SHOULD read `memory.events`
and fail an attempt under sustained `high` throttling.

The owning specifications change on acceptance: [runtime](../system/02_RUNTIME.md)
gains the single-enforcing-layer admission rule and the probed capabilities;
[delivery](../system/06_DELIVERY_AND_CONFORMANCE.md) gains the conformance
evidence listed below.

## Delivery

1. Provider: launch document, events, posture comparison, cgroup stop, crash
   recovery, capability probe, delegated user service.
2. Toolchain rootfs with Codex packaged from its static binary, plus Claude
   Code, git and the egress forwarder; per-attempt home and inputs volumes; path
   translation for `HOME`, `MITOS_WORKSPACE` and `GIT_CEILING_DIRECTORIES`.
3. Harness posture: unsandboxed harness under Nucleus, the workspace mount
   chosen by role, and the admission rule.
4. Broker: `--broker-socket` routing, credential injection, per-attempt token
   accounting.
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
