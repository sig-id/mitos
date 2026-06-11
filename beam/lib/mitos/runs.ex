defmodule Mitos.Runs do
  @moduledoc """
  Read side of the run trace store: turns the `.runs/<run_id>/` artifact
  directories written by `Mitos.Trace` (plus live-session state from the
  registry) into the run listing and run detail the viewer consumes via
  `GET /runs` and `GET /runs/:id`.

  The trace writer stays the only writer; this module only reads. A run is
  "live" when a `Mitos.Session` with the same id is still registered —
  the listing merges that in so the viewer can attach its terminal WS.
  """

  @max_events 500

  @doc "Summaries of every run on disk, newest first, with live status merged in."
  def list do
    runs_dir = Path.join(Mitos.Trace.base_dir(), ".runs")

    case File.ls(runs_dir) do
      {:ok, entries} ->
        entries
        |> Enum.map(&summary(Path.join(runs_dir, &1), &1))
        |> Enum.reject(&is_nil/1)
        |> Enum.sort_by(& &1["created_at_ms"], :desc)

      {:error, _} ->
        []
    end
  end

  @doc """
  Full detail for one run: manifest, decoded event log (capped at the last
  #{@max_events} events), and the approvals sidecar.
  """
  def fetch(run_id) when is_binary(run_id) do
    dir = Path.join([Mitos.Trace.base_dir(), ".runs", run_id])

    if safe_id?(run_id) and File.dir?(dir) do
      events = read_jsonl(Path.join(dir, "events.jsonl"))
      approvals = read_jsonl(Path.join(dir, "approvals.jsonl"))
      manifest = read_manifest(Path.join(dir, "run.yaml"))

      {:ok,
       %{
         "run_id" => run_id,
         "manifest" => manifest,
         "live" => live?(run_id),
         "status" => status(run_id, List.last(events)),
         "event_count" => length(events),
         "events" => Enum.take(events, -@max_events),
         "approvals" => approvals
       }}
    else
      {:error, :not_found}
    end
  end

  def fetch(_), do: {:error, :not_found}

  defp summary(dir, run_id) do
    if File.dir?(dir) do
      manifest = read_manifest(Path.join(dir, "run.yaml"))
      {event_count, last_event} = scan_events(Path.join(dir, "events.jsonl"))
      approvals = count_lines(Path.join(dir, "approvals.jsonl"))

      %{
        "run_id" => run_id,
        "provider" => manifest["provider"],
        "task" => manifest["task"],
        "created_at_ms" => int_or_nil(manifest["created_at_ms"]),
        "live" => live?(run_id),
        "status" => status(run_id, last_event),
        "events" => event_count,
        "approvals" => approvals,
        "last_event_ms" => last_event["timestamp_ms"]
      }
    end
  end

  defp live?(run_id), do: Registry.lookup(Mitos.SessionRegistry, run_id) != []

  # Status from the tail of the event log: a live session is running; a dead
  # one is classified by how its process ended.
  defp status(run_id, last_event) do
    cond do
      live?(run_id) -> "running"
      last_event == nil -> "empty"
      true -> exit_status(last_event["event"] || %{})
    end
  end

  defp exit_status(%{"type" => type} = event) when type in ["exit", "PROCESS_EXITED"] do
    case event["code"] do
      0 -> "completed"
      code when is_integer(code) -> "failed"
      _ -> "interrupted"
    end
  end

  defp exit_status(%{"type" => "ERROR"}), do: "error"
  defp exit_status(_), do: "interrupted"

  # One pass over events.jsonl for the listing: count lines, decode only the
  # last one. RAW_REF-heavy PTY runs make full decoding too expensive here.
  defp scan_events(path) do
    case File.read(path) do
      {:ok, raw} ->
        lines = raw |> String.split("\n", trim: true)

        decoded =
          case List.last(lines) && Jason.decode(List.last(lines)) do
            {:ok, %{} = record} -> record
            _ -> nil
          end

        {length(lines), decoded}

      {:error, _} ->
        {0, nil}
    end
  end

  defp count_lines(path) do
    case File.read(path) do
      {:ok, raw} -> raw |> String.split("\n", trim: true) |> length()
      {:error, _} -> 0
    end
  end

  defp read_jsonl(path) do
    case File.read(path) do
      {:ok, raw} ->
        raw
        |> String.split("\n", trim: true)
        |> Enum.flat_map(fn line ->
          case Jason.decode(line) do
            {:ok, %{} = record} -> [record]
            _ -> []
          end
        end)

      {:error, _} ->
        []
    end
  end

  # run.yaml is the flat `key: value` manifest Mitos.Trace writes — not
  # arbitrary YAML, so a line parser is the whole contract.
  defp read_manifest(path) do
    case File.read(path) do
      {:ok, raw} ->
        raw
        |> String.split("\n", trim: true)
        |> Enum.flat_map(fn line ->
          case String.split(line, ": ", parts: 2) do
            [key, value] -> [{key, value}]
            _ -> []
          end
        end)
        |> Map.new()

      {:error, _} ->
        %{}
    end
  end

  defp int_or_nil(value) when is_binary(value) do
    case Integer.parse(value) do
      {int, ""} -> int
      _ -> nil
    end
  end

  defp int_or_nil(_), do: nil

  defp safe_id?(id), do: Regex.match?(~r/^[a-zA-Z0-9][a-zA-Z0-9_-]*$/, id)
end
