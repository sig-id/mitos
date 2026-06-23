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
      status_event = status_source(events)

      {:ok,
       %{
         "run_id" => run_id,
         "manifest" => manifest,
         "live" => live?(run_id),
         "status" => status(run_id, status_event || List.last(events)),
         "event_count" => length(events),
         "events" => limit_events(events, @max_events),
         "approvals" => approvals,
         "proof" => Mitos.Proof.from_events(events)
       }}
    else
      {:error, :not_found}
    end
  end

  def fetch(_), do: {:error, :not_found}

  @doc "Decoded canonical event records for one run, uncapped by default."
  def events(run_id, opts \\ [])

  def events(run_id, opts) when is_binary(run_id) and is_list(opts) do
    dir = Path.join([Mitos.Trace.base_dir(), ".runs", run_id])

    if safe_id?(run_id) and File.dir?(dir) do
      events = read_jsonl(Path.join(dir, "events.jsonl"))
      {:ok, limit_events(events, Keyword.get(opts, :limit, :all))}
    else
      {:error, :not_found}
    end
  end

  def events(_run_id, _opts), do: {:error, :not_found}

  @doc "Decoded canonical event records across all known runs."
  def all_events(opts \\ []) when is_list(opts) do
    limit = Keyword.get(opts, :per_run_limit, :all)

    list()
    |> Enum.flat_map(fn %{"run_id" => run_id} ->
      case events(run_id, limit: limit) do
        {:ok, events} -> events
        {:error, _reason} -> []
      end
    end)
  end

  defp summary(dir, run_id) do
    if File.dir?(dir) do
      manifest = read_manifest(Path.join(dir, "run.yaml"))
      {event_count, last_event, status_event} = scan_events(Path.join(dir, "events.jsonl"))
      approvals = count_lines(Path.join(dir, "approvals.jsonl"))

      %{
        "run_id" => run_id,
        "provider" => manifest["provider"],
        "task" => manifest["task"],
        "created_at_ms" => int_or_nil(manifest["created_at_ms"]),
        "live" => live?(run_id),
        "status" => status(run_id, status_event || last_event),
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
      true -> exit_status(last_event["event"] || last_event || %{})
    end
  end

  defp exit_status(%{"type" => type} = event) when type in ["exit", "PROCESS_EXITED"] do
    case event["code"] do
      0 -> "completed"
      code when is_integer(code) -> "failed"
      _ -> "interrupted"
    end
  end

  defp exit_status(%{"kind" => "decision", "type" => "finish"}), do: "completed"
  defp exit_status(%{"kind" => "decision", "type" => "abort"}), do: "failed"

  defp exit_status(%{"kind" => "effect", "type" => "capability_finished", "payload" => payload}) do
    case payload do
      %{"status" => "failed"} -> "failed"
      %{"status" => "interrupted"} -> "interrupted"
      _ -> "completed"
    end
  end

  defp exit_status(%{"kind" => "effect", "type" => "capability_finished"}), do: "completed"
  defp exit_status(%{"kind" => "effect", "type" => "capability_started"}), do: "interrupted"
  defp exit_status(%{"type" => "ERROR"}), do: "error"
  defp exit_status(_), do: "interrupted"

  # One pass over events.jsonl for the listing: count lines and keep the tail
  # status-bearing event. Proof/billing effects may legally arrive after a
  # capability_finished event, so the literal last event is not always the run
  # lifecycle status.
  defp scan_events(path) do
    case File.read(path) do
      {:ok, raw} ->
        lines = raw |> String.split("\n", trim: true)

        decoded =
          lines
          |> Enum.flat_map(fn line ->
            case Jason.decode(line) do
              {:ok, %{} = record} -> [record]
              _ -> []
            end
          end)

        {length(lines), List.last(decoded), status_source(decoded)}

      {:error, _} ->
        {0, nil, nil}
    end
  end

  defp status_source(events) do
    events
    |> Enum.reverse()
    |> Enum.find(&status_record?/1)
  end

  defp status_record?(%{"event" => event}), do: status_event?(event)
  defp status_record?(record), do: status_event?(record)

  defp status_event?(%{"type" => type}) when type in ["exit", "PROCESS_EXITED", "ERROR"],
    do: true

  defp status_event?(%{"kind" => "decision", "type" => type}) when type in ["finish", "abort"],
    do: true

  defp status_event?(%{"kind" => "effect", "type" => type})
       when type in ["capability_started", "capability_finished"],
       do: true

  defp status_event?(_event), do: false

  defp count_lines(path) do
    case File.read(path) do
      {:ok, raw} -> raw |> String.split("\n", trim: true) |> length()
      {:error, _} -> 0
    end
  end

  defp limit_events(events, :all), do: events

  defp limit_events(events, limit) when is_integer(limit) and limit > 0,
    do: Enum.take(events, -limit)

  defp limit_events(events, _limit), do: events

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
