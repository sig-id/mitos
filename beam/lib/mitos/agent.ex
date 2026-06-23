defmodule Mitos.Agent do
  @moduledoc """
  High-level entry point for running an AI coding agent through the engine
  control protocol.

  The BEAM control plane names a provider, task, and transport preference
  order. Provider-specific argv construction and prompt detection live in the
  Zig engine.
  """

  alias Mitos.SessionSupervisor

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
    session_id = Keyword.get(opts, :session_id, random_id())

    launch_spec =
      %{
        version: 1,
        provider: provider,
        task: task,
        transport_order: transport_order
      }
      |> maybe_put(:argv, opts[:argv])
      |> maybe_put(:capability_ref, opts[:capability_ref])
      |> maybe_put(:seller_id, opts[:seller_id])
      |> maybe_put(:trust_tier, opts[:trust_tier])
      |> maybe_put(:image, opts[:image])
      |> maybe_put(:data, opts[:data])
      |> maybe_put(:harnesses, opts[:harnesses])
      |> maybe_put(:skill, opts[:skill])
      |> maybe_put(:skill_adapt, opts[:skill_adapt])
      |> maybe_put(:resolved_skill, opts[:resolved_skill])
      |> maybe_put(:tools, opts[:tools])
      |> maybe_put(:resources, opts[:resources])
      |> maybe_put(:hooks, opts[:hooks])
      |> maybe_put(:policy, opts[:policy])
      |> maybe_put(:egress_policy, opts[:egress_policy])
      |> maybe_put(:result, opts[:result])
      |> maybe_put(:authority, opts[:authority])
      |> maybe_put(:knowledge, opts[:knowledge])

    with {:ok, trace, opening_decision_seq} <-
           append_opening_decision(session_id, launch_spec, opts),
         {:ok, %{pid: pid, session_id: session_id}} <-
           SessionSupervisor.start_session(
             session_opts(launch_spec, Keyword.put(opts, :session_id, session_id),
               opening_decision_seq: opening_decision_seq,
               trace: trace
             )
           ) do
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

  defp session_opts(launch_spec, opts, extra) do
    [
      launch_spec: launch_spec,
      cwd: opts[:cwd],
      cols: opts[:cols],
      rows: opts[:rows],
      idle_ms: opts[:idle_ms],
      env: opts[:env],
      schema_root: opts[:schema_root],
      sidecar_path: opts[:sidecar_path],
      session_id: opts[:session_id],
      trace_base_dir: opts[:trace_base_dir],
      project_id: opts[:project_id],
      opening_decision_seq: extra[:opening_decision_seq],
      trace: extra[:trace]
    ]
    |> Enum.reject(fn {_k, v} -> is_nil(v) end)
  end

  defp append_opening_decision(session_id, launch_spec, opts) do
    trace =
      Mitos.Trace.open!(
        [
          run_id: session_id,
          launch_spec: launch_spec
        ]
        |> maybe_put_option(:base_dir, opts[:trace_base_dir])
        |> maybe_put_option(:org_id, opts[:org_id])
        |> maybe_put_option(:project_id, opts[:project_id])
        |> maybe_put_option(:actor_id, opts[:actor_id])
      )

    case Mitos.Trace.append_decision_checked(trace, %{
           actor: "orchestrator",
           type: "advance",
           target: "agent:#{launch_spec.provider}",
           params:
             %{
               provider: launch_spec.provider,
               task: launch_spec.task,
               transport_order: launch_spec.transport_order
             }
             |> maybe_put(:capability_ref, Map.get(launch_spec, :capability_ref))
             |> maybe_put(:seller_id, Map.get(launch_spec, :seller_id))
             |> maybe_put(:trust_tier, Map.get(launch_spec, :trust_tier)),
           inputs_hash: inputs_hash(launch_spec),
           rationale: "Launch #{launch_spec.provider} through the local engine",
           budget:
             Keyword.get(opts, :budget, %{
               "tokens" => 0,
               "usd" => 0,
               "wall_ms" => 0,
               "remaining" => %{}
             })
         }) do
      {:ok, trace, %{"seq" => decision_seq}} ->
        {:ok, trace, decision_seq}

      {:abort, _trace, abort} ->
        {:error, {:budget_exceeded, abort}}
    end
  end

  defp inputs_hash(launch_spec) do
    canonical =
      launch_spec
      |> Map.take([
        :provider,
        :task,
        :transport_order,
        :argv,
        :capability_ref,
        :seller_id,
        :trust_tier,
        :image,
        :data,
        :harnesses,
        :skill,
        :skill_adapt,
        :resolved_skill,
        :tools,
        :resources,
        :hooks,
        :policy,
        :egress_policy,
        :result,
        :knowledge
      ])
      |> canonical_json()

    digest =
      :crypto.hash(:sha256, canonical)
      |> Base.encode16(case: :lower)

    "sha256:" <> digest
  end

  defp random_id do
    "agent-" <> (:crypto.strong_rand_bytes(8) |> Base.url_encode64(padding: false))
  end

  defp maybe_put_option(opts, _key, nil), do: opts
  defp maybe_put_option(opts, key, value), do: Keyword.put(opts, key, value)

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

  defp canonical_json(%{} = map) do
    body =
      map
      |> Enum.sort_by(fn {key, _value} -> to_string(key) end)
      |> Enum.map_join(",", fn {key, value} ->
        Jason.encode!(to_string(key)) <> ":" <> canonical_json(value)
      end)

    "{" <> body <> "}"
  end

  defp canonical_json(values) when is_list(values) do
    "[" <> Enum.map_join(values, ",", &canonical_json/1) <> "]"
  end

  defp canonical_json(value), do: Jason.encode!(value)
end
