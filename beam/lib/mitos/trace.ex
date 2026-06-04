defmodule Mitos.Trace do
  @moduledoc """
  File-backed run trace owned by the BEAM control plane.

  The engine owns high-volume byte spools and sends references upward. This
  module records the decoded protocol event stream and manifest under
  `.runs/<run_id>/`, matching the product artifact names used by the Zig trace
  layer.
  """

  require Logger

  defstruct [
    :run_id,
    :dir,
    seq: 0
  ]

  @artifact_files ~w(
    events.jsonl
    terminal.snapshots.jsonl
    sandbox.events.jsonl
    resources.jsonl
    commands.jsonl
    approvals.jsonl
    test_results.jsonl
  )

  @doc "Open or initialize a trace directory for a run."
  def open!(opts) do
    run_id = Keyword.fetch!(opts, :run_id)
    base_dir = Keyword.get_lazy(opts, :base_dir, &default_base_dir/0)
    dir = Path.join([base_dir, ".runs", run_id])

    File.mkdir_p!(dir)
    write_manifest!(dir, opts)

    for file <- @artifact_files do
      File.write!(Path.join(dir, file), "")
    end

    File.mkdir_p!(Path.join(dir, "screenshots"))
    File.mkdir_p!(Path.join(dir, "artifacts"))

    %__MODULE__{run_id: run_id, dir: dir}
  end

  def append_event(%__MODULE__{} = trace, event) when is_map(event) do
    record = %{
      "schema_version" => 1,
      "run_id" => trace.run_id,
      "sequence" => trace.seq,
      "timestamp_ms" => System.system_time(:millisecond),
      "event" => normalize(event)
    }

    append_jsonl!(trace, "events.jsonl", record)
    maybe_append_sidecar!(trace, record)
    %{trace | seq: trace.seq + 1}
  rescue
    exception ->
      Logger.warning(
        "failed to append trace event for #{inspect(trace.run_id)}: #{Exception.message(exception)}"
      )

      trace
  end

  def append_snapshot(%__MODULE__{} = trace, snapshot) when is_map(snapshot) do
    snapshot = normalize(snapshot)

    append_jsonl!(trace, "terminal.snapshots.jsonl", %{
      "schema_version" => 1,
      "run_id" => trace.run_id,
      "timestamp_ms" => System.system_time(:millisecond),
      "snapshot" => snapshot
    })

    append_event(trace, %{
      "type" => "SNAPSHOT",
      "seq" => Map.get(snapshot, "seq"),
      "artifact" => %{"kind" => "terminal_snapshots", "path" => "terminal.snapshots.jsonl"}
    })
  rescue
    exception ->
      Logger.warning(
        "failed to append trace snapshot for #{inspect(trace.run_id)}: #{Exception.message(exception)}"
      )

      trace
  end

  def append_command(%__MODULE__{} = trace, event) when is_map(event) do
    record = %{
      "schema_version" => 1,
      "run_id" => trace.run_id,
      "timestamp_ms" => System.system_time(:millisecond),
      "event" => normalize(event)
    }

    append_jsonl!(trace, "commands.jsonl", record)
    trace
  rescue
    exception ->
      Logger.warning(
        "failed to append command trace for #{inspect(trace.run_id)}: #{Exception.message(exception)}"
      )

      trace
  end

  defp default_base_dir do
    Application.get_env(:mitos, :trace_base_dir) ||
      System.get_env("MITOS_TRACE_DIR") ||
      File.cwd!()
  end

  defp write_manifest!(dir, opts) do
    provider =
      opts
      |> Keyword.get(:launch_spec, %{})
      |> provider_from_launch_spec()

    task =
      opts
      |> Keyword.get(:launch_spec, %{})
      |> task_from_launch_spec()

    manifest = [
      "schema_version: 1",
      "run_id: #{Keyword.fetch!(opts, :run_id)}",
      "created_at_ms: #{System.system_time(:millisecond)}",
      "provider: #{provider}",
      "task: #{task}",
      ""
    ]

    File.write!(Path.join(dir, "run.yaml"), Enum.join(manifest, "\n"))
  end

  defp provider_from_launch_spec(%{"provider" => provider}), do: provider
  defp provider_from_launch_spec(%{provider: provider}), do: provider
  defp provider_from_launch_spec(_), do: "raw-command"

  defp task_from_launch_spec(%{"task" => task}), do: task
  defp task_from_launch_spec(%{task: task}), do: task
  defp task_from_launch_spec(_), do: ""

  defp append_jsonl!(trace, file, record) do
    line = Jason.encode!(record)
    File.write!(Path.join(trace.dir, file), line <> "\n", [:append])
  end

  defp maybe_append_sidecar!(trace, %{"event" => %{"type" => type}} = record)
       when type in ["PROCESS_SPAWNED", "PROCESS_EXITED"] do
    append_jsonl!(trace, "commands.jsonl", record)
  end

  defp maybe_append_sidecar!(trace, %{"event" => %{"type" => "ACTION_REQUESTED"} = event}) do
    append_jsonl!(trace, "approvals.jsonl", %{
      "schema_version" => 1,
      "run_id" => trace.run_id,
      "timestamp_ms" => System.system_time(:millisecond),
      "type" => "approval_requested",
      "approval_id" => event["request_id"] || event["action_id"],
      "origin" => event["origin"] || event["source"],
      "method" => event["method"],
      "action" => event["action"],
      "risk" => event["risk"],
      "context" => event["context"]
    })
  end

  defp maybe_append_sidecar!(trace, %{"event" => %{"type" => "AUTHORITY_VERDICT"} = event}) do
    append_jsonl!(trace, "approvals.jsonl", %{
      "schema_version" => 1,
      "run_id" => trace.run_id,
      "timestamp_ms" => System.system_time(:millisecond),
      "type" => "approval_decision",
      "approval_id" => event["request_id"] || event["action_id"],
      "origin" => event["origin"],
      "method" => event["method"],
      "decision" => event["decision"],
      "reason" => event["reason"],
      "payload" => event["payload"]
    })
  end

  defp maybe_append_sidecar!(_trace, _record), do: :ok

  defp normalize(%Msgpax.Bin{data: data}), do: normalize(data)

  defp normalize(%_{} = value), do: inspect(value)

  defp normalize(value) when is_map(value) do
    Map.new(value, fn {key, val} -> {to_string(key), normalize(val)} end)
  end

  defp normalize(value) when is_list(value), do: Enum.map(value, &normalize/1)

  defp normalize(value) when is_binary(value) do
    if String.valid?(value) do
      value
    else
      %{"encoding" => "base64", "data" => Base.encode64(value)}
    end
  end

  defp normalize(value) when is_atom(value), do: to_string(value)
  defp normalize(value), do: value
end
