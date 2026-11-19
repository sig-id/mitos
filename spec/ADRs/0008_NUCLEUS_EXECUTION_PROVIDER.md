# ADR 0008: Nucleus as the enforcing local execution provider

Status: proposed, 2026-11-19. Evaluated against Nucleus 0.5.3, re-verified against
Nucleus main `f32a554` (unreleased) after the fixes listed below.
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
0.5.3 column is the original evaluation; the main column is after the fixes,
checked with the Mitos integration tests and direct runs.

| Property | 0.5.3 | main `f32a554` |
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
| `/` | Pinned agent toolchain rootfs (Nix store path recorded in the posture) containing the harness CLIs, git and a CA bundle |
| `/workspace` | The Mitos workspace copy, mounted read-only or read-write by role; this replaces the harness's read-only and workspace-write modes |
| `/home/agent` | The per-attempt home, a caller-owned read-write volume holding Mitos-generated harness configuration |
| `/inputs` | Dependency inputs, read-only |

No host toolchain is bound. Caller-owned volumes are not executable, so
executables come only from the rootfs.

**Egress and credentials.** The network mode is `none` with `--broker-socket`
and `--broker-proxy-listen 127.0.0.1:3128`: Nucleus listens on loopback inside
the container and relays each connection to a per-attempt host broker over the
socket, which is not mounted. The broker allowlists model endpoints, adds the credential,
makes the TLS connection upstream and counts tokens per attempt. No model
credential enters the sandbox. This is required rather than optional: without
a harness sandbox, every command the agent runs can read the harness
environment. Counting at the broker gives `workflow.tokens` an enforcement
point.

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
`/context` could not have run. The provider requires the first Nucleus release
after 0.5.3, which carries the start gate.

1. Provider: launch document, events, posture comparison, cgroup stop, crash
   recovery, capability probe, delegated user service.
2. Toolchain rootfs with Codex packaged from its static binary, plus Claude
   Code, git and a CA bundle; per-attempt home and inputs volumes; path
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
