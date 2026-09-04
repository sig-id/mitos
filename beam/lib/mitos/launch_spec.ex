defmodule Mitos.LaunchSpec do
  @moduledoc """
  What a provider needs to materialize a run (05 §2). Compiled from the
  capability manifest + trust_tier + run context by the launch policy compiler
  (`Mitos.LaunchPolicy`). **It carries no secrets** — `egress_policy` names
  credentials by id only; the real material lives at the broker's vault
  (01 §8.5).
  """

  @enforce_keys [:run_id, :capability_ref, :trust_tier, :provider, :harness]
  defstruct run_id: nil,
            # capability_id — what the log, metering, and replay key on (03 §9)
            capability_ref: nil,
            # the loaded Mitos.Capability.Manifest (local providers read dir/skill/tools from it)
            manifest: nil,
            trust_tier: "T1",
            # the selected Mitos.SandboxProvider module
            provider: nil,
            # oci ref (or dir: digest for local dev)
            image: nil,
            # %{source: <path|git-ref|nil>, mode: :rw, materialize: :clone|:in_place}
            # Input, cloned IN at prepare (05 §5.1) — unless the operator turned
            # the sandbox off for their own workspace, which T2 refuses
            # (Mitos.Workspace.mode/2).
            workspace: %{source: nil, mode: :rw, materialize: :clone},
            # %{snapshot, mount, ro} | nil
            data: nil,
            # [ctx-slice ref] — curated context (07), RO
            context: [],
            # the injected harness name (mitos picks ONE from manifest.harnesses)
            harness: nil,
            # the resolved harness adapter module
            harness_mod: nil,
            # %{net_mode, mounts, broker_endpoint, resources, timeouts}
            posture: %{},
            # [%{dest, credential, requires_approval}] — IDs only, no secret material
            egress_policy: [],
            # stage this launch realizes (stamped by the run server; part of idempotency key)
            stage_id: nil,
            # tenant scope for broker credential lookup (seller org for marketplace runs)
            scope: nil

  @type t :: %__MODULE__{}
end
