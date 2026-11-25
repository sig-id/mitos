# ADR 0011 – macOS confinement runs each worker in an apple/container VM

Status: decided 2026-11-24; resolves [issue #103](https://github.com/sig-id/mitos/issues/103).
Extends [ADR 0008](0008_NUCLEUS_EXECUTION_PROVIDER.md).

## Decision

macOS confines local workers with `apple/container`: every confined launch runs
inside its own lightweight Linux virtual machine (Virtualization.framework,
OCI images), launched through the `container` CLI behind the existing
`Confinement` interface. Callers and the fail-closed admission logic in
`internal/compute/policy.go` do not change: the provider still declares
`isolation: "container"`, because the admission contract describes the enforced
posture (minimal mounts, withheld or mediated network, private PID tree) and a
per-container VM is the strongest available implementation of that posture on
macOS, not a new one.

Linux keeps bubblewrap. The confinement tool is selected per platform
(`compute.DefaultConfinementTool()`): `bwrap` on Linux, `apple-container` on
macOS. Hosts that cannot run the selected tool – no `container` CLI, no OCI
image, Intel Macs, macOS older than 26 – keep the explicit fail-closed errors;
nothing silently degrades to advisory execution.

## What carries over unchanged

| Contract piece | apple/container mechanism |
| --- | --- |
| Minimal explicit mounts | `--volume` virtiofs binds of the writable workspace copy, the per-attempt runtime home, tmp, and read-only dependency inputs; nothing else is host-visible |
| Withheld network | `--network none` (reserved name, no interface at all) |
| Mediated egress | the host egress relay keeps serving its allowlisted HTTP CONNECT on a UNIX socket; `--volume <socket>:<socket>` – apple/container converts socket bind mounts into vsock-backed `UnixSocketConfiguration(direction: .into)` – and the in-guest `mitos egress-loop` forwarder still listens on `127.0.0.1:3128`, so the workload's `HTTPS_PROXY` wiring is byte-identical to Linux |
| Loopback operator API unreachable | the guest has no route to host loopback in every mode; mediated runs additionally have no interface at all |
| Independent workspace-write observation | host-side scanning of the virtiofs share; acceptance reconstruction is unchanged |
| Fail-closed admission | `NewConfinement` refuses when the CLI is missing; `confineArgv` refuses when the image or the mediated-egress prerequisites are absent |

The mediated-egress equivalence rests on apple/container's documented socket
handling: a bind mount whose source is a UNIX socket is transparently replaced
by a vsock forwarder (`Sources/Services/RuntimeLinux/Server/RuntimeService.swift`,
`configureContainer`). The relay therefore never listens on a TCP interface and
the allowlist stays the only route out.

## What changes

1. **Toolchain model inverts.** The Linux sandbox bind-mounts host toolchains
   read-only; a Linux guest cannot run macOS binaries, so every workload runs
   from an OCI image that must contain its toolchain – or from one shared
   host tools directory mounted read-only into every VM (`ToolsDir`), which
   restores the bwrap "one copy, N sandboxes" economics for any toolchain
   that ships Linux binaries (pi's npm package with platform prebuilds, for
   example). The image contract is: agent binaries under
   `/usr/local/bin/<name>` (the confinement rewrites the workload's `argv[0]`
   to `GuestBinDir` + basename), a Linux `mitos` build at
   `/usr/local/bin/mitos` for the egress loop, and `/bin/sh`; with a tools
   directory the same slots resolve into it instead. Operators build or pull
   the image and name it via `MITOS_CONFINEMENT_IMAGE`; a configured
   confinement without an image fails closed at startup. Per-VM root
   filesystems are APFS copy-on-write clones of the stored image, so N
   concurrent VMs still cost one copy of the toolchain.
2. **Resource caps become real.** The VM enforces `--cpus` and `--memory`, so
   the provider reports `cpu_quota` and `memory_limit` truthfully for
   apple-container where bubblewrap cannot enforce them; posture limits are
   mapped onto the VM allocation.
3. **Process-tree observation is downgraded, honestly.** `kern.procargs2`
   cannot see into the guest kernel: exec digests for confined macOS runs
   sample the host-side `container run` process, i.e. VM activity rather than
   in-guest execs. The observation package keeps its schema; this ADR records
   that `exec_coverage: "sampled_procfs"` means "host-sampled" and, for
   VM-confined launches, reflects the VM process rather than the in-guest
   process tree. Liveness and quiescence (`blocked_on_human`) sampling the VM
   process preserves the deadlines and kill semantics.
4. **No die-with-parent.** bubblewrap's `--die-with-parent` has no
   apple/container equivalent: killing the foreground CLI can orphan a running
   VM. Every launch therefore writes `--cidfile`, and session teardown
   best-effort runs `container kill --signal KILL` and `container delete
   --force` so a deadline-killed task leaves nothing running.

## Alternatives rejected

- **Seatbelt (`sandbox-exec`) with a startup canary probe** – expressible, and
  keeps the host-process observation contract, but the profile format is a
  deprecated private API; the boundary would rest on an unsupported format the
  canary only partially tests. Rejected as a trust boundary.
- **Status quo (Linux-only confinement)** – zero risk, but tenant-scoped
  structured execution stays impossible on macOS and the failure is a product
  gap, not a posture.
- **Nucleus on macOS** – ADR 0008's runtime already provides a stronger
  attested container posture; it remains Linux-only for the same
  namespace/cgroup reasons and is unaffected by this decision.

## Consequences

- Requirements: Apple Silicon, macOS 26 or later, `container` CLI installed
  and started (`container system start`), and a worker image in the local
  store. Non-qualifying hosts fail closed exactly as before.
- Per-launch VM overhead (boot, memory) replaces the near-zero bwrap fork;
  confinement-requiring macOS paths accept it as the cost of a supported API.
- The worker image becomes part of the trusted toolchain surface; it is built
  and named by operators (`examples/container/`), not fetched implicitly.
- Future work (not this decision): a "VM-sampled activity" observation
  primitive if in-guest exec evidence is ever required, and publishing a
  canonical worker image.
