# Provider lifecycle and launch admission

`internal/compute.Provider` now defines preparation, start, normalized lifecycle
stream, control, snapshot, harvest, stop, and capability discovery. Studio worker
commands use that interface for process execution and workspace harvest. Verifier
and measurement commands use the same local process implementation in their
separate verification directories.

## Admission

The deterministic compiler chooses from supplied provider descriptors. An explicit
provider name must be eligible; an unavailable or insufficient provider is never
silently replaced with lower isolation. A strict launch requires enforced
workspace confinement and broker-only networking. T2 additionally requires a
remote microVM, read-only snapshot data, complete metering capability, and copied
workspaces. T2 rejects development execution and `in_place` unconditionally.
Remote brokers require credential-free HTTPS endpoints; local broker descriptors
can use Unix endpoints. Embedded credentials, query parameters, and fragments are
refused in broker endpoints.

Local applications can declare a strict request:

```json
{
  "compute": {
    "trust_tier": "T2",
    "provider": "remote-provider",
    "net_mode": "deny-all-except-broker",
    "workspace_mode": "copy",
    "broker_endpoint": "https://broker.example.invalid"
  }
}
```

This is an admission request, not a way to register a provider. This build registers
only the local development process implementation, so the example is refused
before runnable work is created. Explicit strict T0/T1 requests are also refused
until a provider can enforce their broker and confinement requirements.

Existing local applications that omit `compute` continue through a recorded
**development exception**. Their launch record says `development: true`,
`isolation: process`, `egress: unrestricted`, and `workspace_confined: false`.
The trust-tier label does not turn this exception into sandboxed T1 execution.
It grants no unattended horizon, inaccessible oracle, metered commercial run,
read-only data mount, or broker enforcement.

The execution plan retains the compiled specification. Each worker segment records
`provider_launch_compiled` before starting, including the original deadline and
actual provider descriptor. Provider preparation rechecks its own capabilities
against that specification. Recovery cannot silently change the saved posture.

## Local lifecycle

`Prepare` starts no command. It creates isolated HOME and temporary directories
and pins the launch identity/posture in a synced preparation record. Repeated
preparation of the same handle is idempotent; changed identity or posture is
refused. Source-based preparation can materialize a private Git workspace using
the shared bounded, ignore-aware intake mechanism, preserving the source. Its
baseline commit and fingerprint survive re-prepare and bind harvest.

Studio currently supplies its existing Git/checkpoint-materialized working tree
to `Prepare`; consolidation of initial Studio intake fully behind the provider
remains outstanding. Preparation records are provider metadata, not an alternative
run log. Studio's Decisions and Effects remain authoritative for whether a
command may start again after a crash.

`Start` accepts an absolute executable and non-secret command input. It clears the
inherited environment, supplies the existing hermetic PATH/HOME/TMPDIR/Git settings,
and creates a process group. It refuses a second start of the same prepared handle.
The earlier of the caller deadline and persisted launch deadline bounds execution.
Combined output remains capped at 2 MiB while its total byte offset continues to
advance for observation.

The stream records ordered `status` events for actual process start and exit,
with resumable offsets and exit status. It does not interpret stdout as a harness
semantic event or manufacture a `final_result`. Studio records these as
`provider_status` evidence. Native harness semantic streams and remote reconnection
adapters still require their external integrations.

`Control(interrupt)` stops the process group. Other control commands return
unsupported; a raw approval/answer is not verified steering. `Snapshot` explicitly
returns unsupported. `Stop` is idempotent. Descendant processes in the command's
process group are terminated before completion is returned, preserving the existing
local process limitation for descendants that escape that group.

`Harvest` requires a stopped session and is available after command failure,
cancellation, and deadline expiry. It reads the actual workspace against the
protected baseline and returns the patch, changed paths, and result snapshot;
Studio registers the artifacts and performs independent acceptance. A failure to
start still retains the existing bounded control-plane harvest fallback.

## Remaining compute contracts

This provider is a trusted local process runner. It cannot confine filesystem or
network access, block host credentials/metadata endpoints, supply protected RO
mounts, or enforce CPU/memory quotas. It does not implement in-place recovery.

External engine/provider clients, registered strict providers, broker/vault and
forced network routing, image/data/context injection, enforced oracle isolation,
provider snapshots, full resource metering, native harness observation and steer,
and remote provider availability/reselection remain required. Compiler tests prove
the admission rules over descriptors; they are not evidence of a deployed remote
sandbox or enforced broker. Both Studio and Market product gates remain unmet.
