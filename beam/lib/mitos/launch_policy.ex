defmodule Mitos.LaunchPolicy do
  @moduledoc """
  The Tier-1 launch policy compiler (05 §4) — deterministic, coded, not agentic.

      compile(manifest, trust_tier, run_ctx) ->
        eligible = providers where capabilities() ⊇ required(tier)
        select   = pick(eligible, locality_pref, cost, availability)
        => LaunchSpec{provider, posture: posture_for(tier), egress_policy}

  If no eligible provider exists for a tier the compiler MUST fail the launch
  (emit nothing) rather than silently downgrade isolation — **distrust scales
  isolation; it never relaxes it** (05 §4). Likewise an incompatible harness is
  refused, never degraded (03 §5).
  """

  alias Mitos.{Capability, LaunchSpec}

  # microVM-class isolation mechanisms acceptable for T2 (05 §3/§4).
  @microvm_isolation [:microvm, :firecracker]

  @doc """
  Compile a LaunchSpec. `run_ctx` carries `%{run_id, stage_id, workspace,
  scope, trust_tier?}` — `trust_tier` in ctx overrides the manifest's (the
  facilitator forces T2 at publish, 08 §8; MVP curates sellers at T1, 09 §2).
  """
  def compile(%Capability.Manifest{} = m, run_ctx) do
    tier = run_ctx[:trust_tier] || m.trust_tier

    with {:ok, provider} <- select_provider(tier, run_ctx),
         {:ok, harness_name, harness_mod} <- select_harness(m) do
      {:ok,
       %LaunchSpec{
         run_id: run_ctx.run_id,
         capability_ref: m.capability_id,
         manifest: m,
         trust_tier: tier,
         provider: provider,
         image: m.image || "dir:" <> m.dir,
         workspace: %{source: run_ctx[:workspace], mode: :rw},
         data: m.data,
         context: run_ctx[:context] || [],
         harness: harness_name,
         harness_mod: harness_mod,
         posture: posture_for(tier, m),
         egress_policy: m.egress_policy,
         stage_id: run_ctx[:stage_id],
         scope: run_ctx[:scope]
       }}
    end
  end

  defp select_provider(tier, run_ctx) do
    eligible =
      providers()
      |> Enum.filter(&eligible?(tier, &1.capabilities()))

    case {eligible, run_ctx[:provider]} do
      {[], _} ->
        {:error, {:no_eligible_provider, tier}}

      {list, forced} when not is_nil(forced) ->
        if forced in list, do: {:ok, forced}, else: {:error, {:no_eligible_provider, tier}}

      {[first | _], nil} ->
        {:ok, first}
    end
  end

  # T2 hard constraints (05 §4): remote microVM isolation + broker egress.
  # RO-snapshot data / no host binds are enforced at manifest load + prepare.
  defp eligible?("T2", caps),
    do: caps.remote? and caps.isolation in @microvm_isolation and caps.egress == :broker

  defp eligible?(_t0_or_t1, caps), do: caps.egress == :broker

  # mitos injects exactly ONE harness from the compatible set (01 §5, 03 §5);
  # an unknown set is refused, not degraded.
  defp select_harness(%Capability.Manifest{harnesses: names}) do
    registry = Application.fetch_env!(:mitos, :harnesses)

    case Enum.find(names, &Map.has_key?(registry, &1)) do
      nil -> {:error, {:no_compatible_harness, names}}
      name -> {:ok, name, Map.fetch!(registry, name)}
    end
  end

  defp posture_for(tier, m) do
    %{
      net_mode: :deny_all_except_broker,
      broker_endpoint: Mitos.Broker.Listener.endpoint(),
      # T2: RO provider snapshot only, no host binds (05 §4); T0/T1: bind ok (05 §7)
      mounts: if(tier == "T2", do: :ro_snapshot_only, else: :bind),
      resources: m.resources,
      timeouts: %{wall_ms: parse_timeout(m.resources["timeout"])}
    }
  end

  defp parse_timeout(nil), do: 900_000
  defp parse_timeout(n) when is_integer(n), do: n * 1000

  defp parse_timeout(s) when is_binary(s) do
    case Integer.parse(s) do
      {n, "s"} -> n * 1000
      {n, "ms"} -> n
      {n, _} -> n * 1000
      :error -> 900_000
    end
  end

  defp providers, do: Application.fetch_env!(:mitos, :providers)
end
