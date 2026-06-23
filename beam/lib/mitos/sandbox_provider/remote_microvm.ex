defmodule Mitos.SandboxProvider.RemoteMicrovm do
  @moduledoc """
  Generic HTTP-backed remote microVM provider.

  This is the BEAM-side client seam for T2 providers. Operators point it at a
  provider service with `:mitos, :remote_microvm_endpoint` or
  `MITOS_REMOTE_MICROVM_ENDPOINT`; the provider service owns the actual E2B or
  Daytona VM lifecycle and returns normalized events plus harvest metadata.
  """

  @behaviour Mitos.SandboxProvider

  @impl true
  def prepare(%{} = launch_spec) do
    with {:ok, endpoint} <- endpoint() do
      {:ok, %{endpoint: endpoint, launch_spec: launch_spec}}
    end
  end

  @impl true
  def start(%{endpoint: endpoint, launch_spec: launch_spec}, argv, env) do
    body = %{
      "launch_spec" => normalize(launch_spec),
      "argv" => Enum.map(List.wrap(argv), &to_string/1),
      "env" => env_map(env)
    }

    with {:ok, response} <- post_json(endpoint, "/runs", body) do
      {:ok,
       %{
         endpoint: endpoint,
         remote_run_id: response["run_id"] || response["id"],
         events: list_or(response["events"]),
         harvest: harvest_or(response["harvest"])
       }}
    end
  end

  @impl true
  def stream(%{events: events}), do: events

  @impl true
  def control(_session, _command), do: {:error, :unsupported_control}

  @impl true
  def snapshot(_session), do: :unsupported

  @impl true
  def harvest(%{harvest: harvest}) when is_map(harvest), do: {:ok, normalize_harvest(harvest)}
  def harvest(_session), do: {:error, :harvest_missing}

  @impl true
  def stop(%{endpoint: endpoint, remote_run_id: remote_run_id}) when is_binary(remote_run_id) do
    _ = post_json(endpoint, "/runs/#{URI.encode(remote_run_id)}/stop", %{})
    :ok
  end

  def stop(_session), do: :ok

  @impl true
  def capabilities do
    %{
      isolation: :microvm,
      remote?: true,
      egress: :broker,
      data_mount: :snapshot,
      metering: [:cpu, :mem, :wall, :egress],
      snapshot?: true,
      net_modes: [:broker_proxy]
    }
  end

  @impl true
  def child_spec(_opts) do
    %{
      id: __MODULE__,
      start: {Task, :start_link, [fn -> :ok end]},
      restart: :temporary
    }
  end

  defp endpoint do
    endpoint =
      Application.get_env(:mitos, :remote_microvm_endpoint) ||
        System.get_env("MITOS_REMOTE_MICROVM_ENDPOINT")

    if is_binary(endpoint) and endpoint != "" do
      {:ok, String.trim_trailing(endpoint, "/")}
    else
      {:error, :remote_microvm_endpoint_not_configured}
    end
  end

  defp post_json(endpoint, path, body) do
    :ok = ensure_http_started()
    url = String.to_charlist(endpoint <> path)
    headers = [{~c"content-type", ~c"application/json"}]
    request = {url, headers, ~c"application/json", Jason.encode!(body)}

    case :httpc.request(:post, request, [], body_format: :binary) do
      {:ok, {{_http, status, _reason}, _headers, response_body}} when status in 200..299 ->
        case Jason.decode(response_body) do
          {:ok, %{} = decoded} -> {:ok, decoded}
          {:ok, _other} -> {:error, :invalid_remote_response}
          {:error, error} -> {:error, {:invalid_remote_json, Exception.message(error)}}
        end

      {:ok, {{_http, status, _reason}, _headers, response_body}} ->
        {:error, {:remote_provider_error, status, response_body}}

      {:error, reason} ->
        {:error, {:remote_provider_unavailable, reason}}
    end
  end

  defp ensure_http_started do
    _ = Application.ensure_all_started(:inets)
    _ = Application.ensure_all_started(:ssl)
    :ok
  end

  defp env_map(nil), do: %{}

  defp env_map(%{} = env) do
    Map.new(env, fn {key, value} -> {to_string(key), to_string(value)} end)
  end

  defp env_map(env) when is_list(env) do
    Enum.flat_map(env, fn
      {key, value} -> [{to_string(key), to_string(value)}]
      %{"name" => key, "value" => value} -> [{to_string(key), to_string(value)}]
      %{name: key, value: value} -> [{to_string(key), to_string(value)}]
      _ -> []
    end)
    |> Map.new()
  end

  defp env_map(_env), do: %{}

  defp list_or(list) when is_list(list), do: Enum.filter(list, &is_map/1)
  defp list_or(_other), do: []

  defp harvest_or(%{} = harvest), do: harvest
  defp harvest_or(_other), do: nil

  defp normalize_harvest(%{} = harvest) do
    %{
      diff: map_or(harvest["diff"] || harvest[:diff], %{"empty?" => true, "files_changed" => []}),
      result: map_or(harvest["result"] || harvest[:result], %{"signal" => "unknown"}),
      artifacts: list_or(harvest["artifacts"] || harvest[:artifacts] || [])
    }
  end

  defp map_or(%{} = map, _default), do: normalize(map)
  defp map_or(_other, default), do: default

  defp normalize(%{} = map),
    do: Map.new(map, fn {key, value} -> {to_string(key), normalize(value)} end)

  defp normalize(list) when is_list(list), do: Enum.map(list, &normalize/1)
  defp normalize(value), do: value
end
