defmodule Mitos.SandboxPolicy do
  @moduledoc """
  Launch policy compiler for trust-tier to SandboxProvider eligibility.

  The policy is deliberately fail-closed: T2 requires a remote microVM-class
  provider with broker-only egress. Local sidecar execution is acceptable for
  local/dev T1 work, but never for marketplace T2.
  """

  @microvm_isolations [
    :microvm,
    :firecracker,
    :e2b,
    :daytona,
    "microvm",
    "firecracker",
    "e2b",
    "daytona"
  ]
  @t2_metering ~w(cpu mem wall egress)

  @doc "Return `:ok` or an error explaining why no provider can satisfy a workload's trust tier."
  def ensure_workload_allowed(%Mitos.Workload{capability: nil}), do: :ok

  def ensure_workload_allowed(%Mitos.Workload{capability: %Mitos.Capability{} = capability}) do
    ensure_trust_tier_allowed(capability.trust_tier)
  end

  @doc "Select an executable provider for a workload's trust tier."
  def select_workload_provider(%Mitos.Workload{capability: nil}), do: select_provider("T1")

  def select_workload_provider(%Mitos.Workload{capability: %Mitos.Capability{} = capability}) do
    select_provider(capability.trust_tier)
  end

  @doc "Return `:ok` if any configured provider can satisfy the trust tier."
  def ensure_trust_tier_allowed(nil), do: :ok
  def ensure_trust_tier_allowed("T0"), do: eligible_or_error("T0")
  def ensure_trust_tier_allowed("T1"), do: eligible_or_error("T1")
  def ensure_trust_tier_allowed("T2"), do: eligible_or_error("T2")
  def ensure_trust_tier_allowed(other), do: {:error, {:invalid_trust_tier, other}}

  @doc "Compile the network/mount/metering posture for a trust tier."
  def launch_posture(tier, launch_spec \\ %{})

  def launch_posture(nil, launch_spec), do: launch_posture("T1", launch_spec)

  def launch_posture("T0", launch_spec) when is_map(launch_spec) do
    launch_spec
    |> base_posture("T0")
    |> Map.merge(%{
      "data_mount" => "bind",
      "metering" => "internal",
      "workspace" => %{"mode" => "rw", "host_bind" => true}
    })
  end

  def launch_posture("T1", launch_spec) when is_map(launch_spec) do
    launch_spec
    |> base_posture("T1")
    |> Map.merge(%{
      "data_mount" => "bind",
      "metering" => "internal",
      "workspace" => %{"mode" => "rw", "host_bind" => true}
    })
  end

  def launch_posture("T2", launch_spec) when is_map(launch_spec) do
    launch_spec
    |> base_posture("T2")
    |> Map.merge(%{
      "data_mount" => "snapshot",
      "metering" => "billing_ledger",
      "metered_dimensions" => @t2_metering,
      "workspace" => %{"mode" => "rw", "ephemeral" => true, "host_bind" => false},
      "data" => %{"mode" => "ro", "source" => "snapshot_or_volume", "host_bind" => false}
    })
  end

  def launch_posture(other, _launch_spec), do: %{"trust_tier" => to_string(other)}

  defp eligible_or_error(tier) do
    case eligible_providers(tier) do
      [] ->
        {:error,
         {:sandbox_provider_ineligible, tier,
          %{
            reason: reason(tier),
            providers: provider_summaries()
          }}}

      _providers ->
        :ok
    end
  end

  defp select_provider(tier) do
    case executable_eligible_providers(tier) do
      [provider | _] ->
        {:ok, provider}

      [] ->
        case eligible_providers(tier) do
          [] ->
            {:error,
             {:sandbox_provider_ineligible, tier,
              %{
                reason: reason(tier),
                providers: provider_summaries()
              }}}

          _non_executable ->
            {:error,
             {:sandbox_provider_ineligible, tier,
              %{
                reason: "eligible sandbox provider is not executable from this BEAM node",
                providers: provider_summaries()
              }}}
        end
    end
  end

  defp executable_eligible_providers(tier) do
    configured_providers()
    |> Enum.filter(&executable_provider?/1)
    |> Enum.filter(fn provider ->
      provider
      |> provider_capabilities()
      |> provider_eligible?(tier)
    end)
  end

  defp eligible_providers(tier) do
    Enum.filter(configured_providers(), fn provider ->
      provider
      |> provider_capabilities()
      |> provider_eligible?(tier)
    end)
  end

  defp configured_providers do
    case Application.fetch_env(:mitos, :sandbox_providers) do
      {:ok, providers} -> providers
      :error -> default_providers()
    end
  end

  defp default_providers do
    if remote_microvm_configured?() do
      [Mitos.SandboxProvider.LocalSidecar, Mitos.SandboxProvider.RemoteMicrovm]
    else
      [Mitos.SandboxProvider.LocalSidecar]
    end
  end

  defp remote_microvm_configured? do
    endpoint =
      Application.get_env(:mitos, :remote_microvm_endpoint) ||
        System.get_env("MITOS_REMOTE_MICROVM_ENDPOINT")

    is_binary(endpoint) and endpoint != ""
  end

  defp broker_endpoint_configured?, do: is_binary(broker_endpoint())

  defp broker_endpoint do
    case Application.get_env(:mitos, :broker_endpoint) || System.get_env("MITOS_BROKER_ENDPOINT") do
      endpoint when is_binary(endpoint) ->
        endpoint = String.trim(endpoint)
        if endpoint == "", do: nil, else: String.trim_trailing(endpoint, "/")

      _other ->
        nil
    end
  end

  defp broker_ca_ref do
    case Application.get_env(:mitos, :broker_ca_ref) || System.get_env("MITOS_BROKER_CA_REF") do
      ca_ref when is_binary(ca_ref) ->
        ca_ref = String.trim(ca_ref)
        if ca_ref == "", do: nil, else: ca_ref

      _other ->
        nil
    end
  end

  defp provider_capabilities(provider) when is_atom(provider) do
    if Code.ensure_loaded?(provider) and function_exported?(provider, :capabilities, 0) do
      provider.capabilities()
    else
      %{}
    end
  end

  defp provider_capabilities(%{} = capabilities), do: capabilities
  defp provider_capabilities(_provider), do: %{}

  defp executable_provider?(provider) when is_atom(provider) do
    Code.ensure_loaded?(provider) and
      Enum.all?([:prepare, :start, :stream, :harvest, :stop, :capabilities], fn name ->
        arity =
          case name do
            :start -> 3
            :stop -> 1
            :prepare -> 1
            :stream -> 1
            :harvest -> 1
            :capabilities -> 0
          end

        function_exported?(provider, name, arity)
      end)
  end

  defp executable_provider?(_provider), do: false

  defp provider_eligible?(capabilities, "T2") do
    broker_endpoint_configured?() and
      truthy?(field(capabilities, :remote?)) and
      field(capabilities, :egress) == :broker and
      field(capabilities, :isolation) in @microvm_isolations and
      field(capabilities, :data_mount) in [
        :snapshot,
        :volume,
        :ro_volume,
        "snapshot",
        "volume",
        "ro_volume"
      ] and
      :host not in List.wrap(field(capabilities, :net_modes, [])) and
      metering_eligible?(field(capabilities, :metering, []))
  end

  defp provider_eligible?(capabilities, "T1") do
    field(capabilities, :egress) == :broker and
      field(capabilities, :isolation) in [
        :local_process,
        :nucleus,
        :docker,
        :microvm,
        "local_process",
        "nucleus",
        "docker",
        "microvm"
      ]
  end

  defp provider_eligible?(capabilities, "T0") do
    provider_eligible?(capabilities, "T1")
  end

  defp provider_summaries do
    Enum.map(configured_providers(), fn provider ->
      capabilities = provider_capabilities(provider)

      %{
        provider: provider_name(provider),
        isolation: field(capabilities, :isolation),
        remote?: field(capabilities, :remote?),
        egress: field(capabilities, :egress),
        data_mount: field(capabilities, :data_mount),
        metering: field(capabilities, :metering, []),
        net_modes: field(capabilities, :net_modes, [])
      }
    end)
  end

  defp provider_name(provider) when is_atom(provider), do: inspect(provider)
  defp provider_name(%{} = provider), do: inspect(provider)
  defp provider_name(provider), do: to_string(provider)

  defp reason("T2") do
    if broker_endpoint_configured?() do
      "T2 requires a remote microVM-class provider with broker-only egress, no host network, and CPU/mem/wall/egress metering"
    else
      "T2 requires a configured mitos broker endpoint for forced-proxy egress"
    end
  end

  defp reason(tier), do: "#{tier} has no eligible sandbox provider"

  defp base_posture(launch_spec, tier) do
    %{
      "trust_tier" => tier,
      "net_mode" => "deny_all_except_broker",
      "egress" => "broker",
      "host_network" => false,
      "loopback_bypass" => false
    }
    |> maybe_put("broker_endpoint", broker_endpoint())
    |> maybe_put("ca_ref", broker_ca_ref())
    |> maybe_put("resources", field(launch_spec, :resources))
    |> maybe_put("timeouts", timeout_posture(field(launch_spec, :resources, %{})))
  end

  defp timeout_posture(%{} = resources) do
    resources
    |> field(:timeout)
    |> case do
      nil -> nil
      timeout -> %{"wall" => timeout}
    end
  end

  defp timeout_posture(_resources), do: nil

  defp field(%{} = map, key, default \\ nil) do
    Map.get(map, key, Map.get(map, to_string(key), default))
  end

  defp maybe_put(map, _key, nil), do: map
  defp maybe_put(map, key, value), do: Map.put(map, key, value)

  defp metering_eligible?(metering) do
    advertised =
      metering
      |> List.wrap()
      |> Enum.map(&to_string/1)
      |> MapSet.new()

    Enum.all?(@t2_metering, &MapSet.member?(advertised, &1))
  end

  defp truthy?(value), do: value in [true, "true", 1, "1"]
end
