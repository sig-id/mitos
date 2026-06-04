defmodule Mitos.Agent do
  @moduledoc """
  High-level entry point for running an AI coding agent through the engine
  control protocol.

  The BEAM control plane names a provider, task, and transport preference
  order. Provider-specific argv construction and prompt detection live in the
  Zig engine.
  """

  alias Mitos.Orchestrator

  @default_transport_order ["stdio-json", "pty"]

  @type run :: %{
          session_id: String.t(),
          pid: pid(),
          provider: String.t(),
          adapter: nil,
          mode: :engine,
          transport_order: [String.t()]
        }

  @spec start(keyword()) :: {:ok, run()} | {:error, term()}
  def start(opts) do
    provider = Keyword.fetch!(opts, :provider)
    task = Keyword.fetch!(opts, :task)
    transport_order = transport_order(opts)

    launch_spec =
      %{
        version: 1,
        provider: provider,
        task: task,
        transport_order: transport_order
      }
      |> maybe_put(:argv, opts[:argv])
      |> maybe_put(:policy, opts[:policy])
      |> maybe_put(:authority, opts[:authority])

    with {:ok, %{pid: pid, session_id: session_id}} <-
           Orchestrator.start_session(session_opts(launch_spec, opts)) do
      {:ok,
       %{
         session_id: session_id,
         pid: pid,
         provider: provider,
         adapter: nil,
         mode: :engine,
         transport_order: transport_order
       }}
    end
  end

  def default_transport_order, do: @default_transport_order

  defp session_opts(launch_spec, opts) do
    [
      launch_spec: launch_spec,
      cwd: opts[:cwd],
      cols: opts[:cols],
      rows: opts[:rows],
      idle_ms: opts[:idle_ms],
      env: opts[:env],
      session_id: opts[:session_id]
    ]
    |> Enum.reject(fn {_k, v} -> is_nil(v) end)
  end

  defp transport_order(opts) do
    opts
    |> Keyword.get(:transport_order, Keyword.get(opts, :adapter_order, @default_transport_order))
    |> Enum.map(&normalize_transport/1)
    |> Enum.reject(&is_nil/1)
    |> case do
      [] -> @default_transport_order
      order -> order
    end
  end

  defp normalize_transport(name) when is_binary(name) do
    case name do
      "pty" -> "pty"
      "stdio-json" -> "stdio-json"
      "stdio_json" -> "stdio-json"
      "headless_json" -> "stdio-json"
      "exec_json" -> "stdio-json"
      "acp" -> "acp"
      "mcp" -> "mcp"
      "http-stream" -> "http-stream"
      "http_stream" -> "http-stream"
      "cloud-job" -> "cloud-job"
      "cloud_job" -> "cloud-job"
      _ -> nil
    end
  end

  defp normalize_transport(:pty), do: "pty"
  defp normalize_transport(:headless_json), do: "stdio-json"
  defp normalize_transport(:exec_json), do: "stdio-json"
  defp normalize_transport(:native_api), do: nil
  defp normalize_transport(:raw_shell), do: nil
  defp normalize_transport(:stdio_json), do: "stdio-json"
  defp normalize_transport(:stdio), do: "stdio-json"
  defp normalize_transport(_), do: nil

  defp maybe_put(map, _key, nil), do: map
  defp maybe_put(map, key, value), do: Map.put(map, key, value)
end
