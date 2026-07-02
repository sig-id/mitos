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

  # Run-id prefixes that are user-facing in the Runs list. Internal traces
  # (`wl-*` workload/harness subprocesses, `sess-*` raw PTY sessions) are
  # implementation details of a run and MUST NOT surface here — otherwise the
  # list is drowned in one-per-capability harness noise.
  @public_prefixes ~w(app- agent-)

  defp public_run?(run_id) when is_binary(run_id) do
    Enum.any?(@public_prefixes, &String.starts_with?(run_id, &1))
  end

  defp public_run?(_), do: false

  @doc "Summaries of every run on disk, newest first, with live status merged in."
  def list do
    with {:ok, runs_dir} <- trusted_runs_dir(),
         {:ok, entries} <- File.ls(runs_dir) do
      entries
      |> Enum.map(&summary(Path.join(runs_dir, &1), &1))
      |> Enum.reject(&is_nil/1)
      |> Enum.filter(&public_run?(&1["run_id"]))
      |> Enum.sort_by(& &1["created_at_ms"], :desc)
    else
      _ -> []
    end
  end

  @doc """
  Full detail for one run: manifest, decoded event log (capped at the last
  #{@max_events} events), and the approvals sidecar.
  """
  def fetch(run_id) when is_binary(run_id) do
    cond do
      not public_run?(run_id) ->
        {:error, :not_found}

      not Mitos.Trace.valid_run_id?(run_id) ->
        {:error, :not_found}

      true ->
        case trusted_run_dir(run_id) do
          {:ok, dir} ->
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

          _ ->
            {:error, :not_found}
        end
    end
  end

  def fetch(_), do: {:error, :not_found}

  # ------------------------------------------------------------------
  # Per-step detail for an application run.
  #
  # An `app-*` run is a thin envelope: signal → enqueued → scheduled →
  # running → done/failed. The actual per-capability work happens in child
  # `wl-*` traces (one per pipeline step), linked to the parent by the wl
  # task.id prefix `<parent-task-id>:<step-name>`. Each wl trace owns its
  # capability_started/finished events and the `runtime/workspace/RESULT`
  # artifact the capability wrote.
  # ------------------------------------------------------------------

  @doc ""
  # `step_traces/1` is defined further below.
  # (anchor for readers)
  # """
  def step_traces(run_id), do: step_traces(run_id, [])

  @doc """
  The ordered list of pipeline steps (capabilities) that executed for an app
  run, each with its child trace id, lifecycle status, RESULT artifact (if any),
  and a parsed failure reason.

  Returns `[]` for non-app runs or runs with no child traces yet (a run that
  just launched).
  """
  def step_traces(run_id, opts) when is_binary(run_id) and is_list(opts) do
    with {:ok, _dir} <- trusted_run_dir(run_id),
         {:ok, events} <- events(run_id),
         {:ok, parent_task_id} <- parent_task_id(events) do
      prefix = parent_task_id <> ":"
      steps = find_child_steps(prefix)
      Enum.sort_by(steps, & &1[:order])
    else
      _ -> []
    end
  end

  def step_traces(_run_id, _opts), do: []

  defp parent_task_id(events) do
    case Enum.find(events, &(&1["type"] == "task_enqueued")) do
      %{"payload" => %{"task" => %{"id" => id}}} when is_binary(id) -> {:ok, id}
      _ -> {:error, :no_task}
    end
  end

  defp find_child_steps(prefix) do
    runs_dir = runs_dir!()

    case File.ls(runs_dir) do
      {:ok, entries} ->
        entries
        |> Enum.filter(&String.starts_with?(&1, "wl-"))
        |> Enum.flat_map(fn wl_id -> load_child_step(runs_dir, wl_id, prefix) end)

      _ ->
        []
    end
  end

  defp load_child_step(runs_dir, wl_id, prefix) do
    dir = Path.join(runs_dir, wl_id)

    case read_jsonl(Path.join(dir, "events.jsonl")) do
      events when is_list(events) and events != [] ->
        case step_task_id(events) do
          %{"id" => task_id} when is_binary(task_id) and byte_size(task_id) > 0 ->
            if String.starts_with?(task_id, prefix) do
              step_name = task_step_name(task_id, prefix)
              [build_step(wl_id, dir, events, step_name, task_id)]
            else
              []
            end

          _ ->
            []
        end

      _ ->
        []
    end
  end

  defp step_task_id(events) do
    case Enum.find(events, &(&1["type"] == "task_enqueued")) do
      %{"payload" => %{"task" => %{} = task}} -> task
      _ -> %{}
    end
  end

  defp task_step_name(task_id, prefix) do
    raw = String.trim_leading(task_id, prefix)
    # the step id may itself carry a numeric suffix like "verify-fix-2" from
    # the pipeline runner; keep it for ordering but show the clean name.
    raw
  end

  defp build_step(wl_id, dir, events, step_name, _task_id) do
    result_path = Path.join([dir, "runtime", "workspace", "RESULT"])
    result = read_result(result_path)
    {order, clean_name} = split_step_suffix(step_name)
    manifest = read_manifest(Path.join(dir, "run.yaml"))

    %{
      wl_id: wl_id,
      name: clean_name,
      raw_name: step_name,
      order: order,
      status: step_status(events),
      reason: step_reason(events),
      result: result,
      created_at_ms: int_or_nil(manifest["created_at_ms"]),
      events: length(events)
    }
  end

  defp split_step_suffix(name) do
    case Regex.run(~r/^(.*?)(?:-(\d+))?$/u, name, capture: :all_but_first) do
      [base, num] -> {String.to_integer(num), base}
      [base] -> {0, base}
      _ -> {0, name}
    end
  end

  defp step_status(events) do
    cond do
      Enum.any?(events, &(&1["type"] == "task_failed")) -> "failed"
      Enum.any?(events, &(&1["type"] in ["task_done", "capability_finished"])) -> "completed"
      Enum.any?(events, &(&1["type"] == "capability_started")) -> "running"
      Enum.any?(events, &(&1["type"] == "task_running")) -> "running"
      Enum.any?(events, &(&1["type"] == "task_scheduled")) -> "scheduled"
      true -> "queued"
    end
  end

  # The failure reason lives on the parent app run's `task_failed` effect as a
  # stringified Elixir tuple like
  # `{:pipeline_step_failed, "verify-fix-2", "verify-fix", {:empty_result, "wl-x", [...]}}`.
  # `format_reason/1` parses that into readable prose; here we surface the raw
  # wl-level reason if present.
  defp step_reason(events) do
    case Enum.find(events, &(&1["type"] == "task_failed")) do
      %{"payload" => %{"reason" => reason}} when is_binary(reason) -> reason
      _ -> nil
    end
  end

  defp read_result(path) do
    case read_regular_file(path) do
      {:ok, raw} ->
        case Jason.decode(raw) do
          {:ok, %{} = decoded} -> decoded
          _ -> nil
        end

      _ ->
        nil
    end
  end

  @doc """
  Parse a stringified Elixir error tuple (as carried on `task_failed.reason`)
  into a human-readable summary + detail. Returns `%{summary, detail}` or
  `%{summary: raw}` when the shape isn't recognized.
  """
  def format_reason(nil), do: nil

  def format_reason(raw) when is_binary(raw) do
    cond do
      String.contains?(raw, "pipeline_step_failed") ->
        parse_pipeline_step_failed(raw)

      String.contains?(raw, "empty_result") ->
        parse_empty_result(raw)

      String.contains?(raw, "publication_validation_failed") ->
        %{summary: "Capability failed its publication validation gate",
          detail: String.trim(raw, "{") |> String.trim_trailing("}")}

      true ->
        %{summary: String.slice(raw, 0, 160), detail: raw}
    end
  end

  defp parse_pipeline_step_failed(raw) do
    # {:pipeline_step_failed, "<step-id>", "<capability>", <inner>}
    inner =
      case Regex.run(~r/pipeline_step_failed,\s*"([^"]+)",\s*"([^"]+)",\s*(.+)$/s,
             String.slice(raw, 0, 600)) do
        [_, step, capability, rest] -> {step, capability, rest}
        _ -> {nil, nil, raw}
      end

    {step, capability, rest} = inner
    base = parse_inner_error(String.trim_trailing(rest, "}"))

    summary =
      ["Step \"#{step || "?"}\" failed", base && "— #{base.summary}"]
      |> Enum.reject(&is_nil/1)
      |> Enum.join(" ")

    detail_parts =
      [
        capability && "capability: #{capability}",
        base && base[:detail]
      ]
      |> Enum.reject(&is_nil/1)

    %{summary: summary, detail: Enum.join(detail_parts, " · "), capability: capability}
  end

  defp parse_inner_error(rest) do
    cond do
      String.contains?(rest, "empty_result") ->
        # {:empty_result, "<wl-id>", ["Reason1", ...]}
        wl =
          case Regex.run(~r/empty_result,\s*"(wl-[^"]+)"/, rest) do
            [_, id] -> id
            _ -> nil
          end

        reasons =
          Regex.scan(~r/"([^"]+)"/, rest)
          |> Enum.map(fn [_, s] -> s end)
          |> Enum.reject(&String.starts_with?(&1, "wl-"))

        reason_str = if reasons == [], do: "", else: " (" <> Enum.join(reasons, ", ") <> ")"

        %{
          summary: "the agent produced no RESULT#{reason_str}",
          detail: if(wl, do: "trace: #{wl}", else: nil)
        }

      String.contains?(rest, "timeout") ->
        %{summary: "the step timed out", detail: nil}

      true ->
        clean = String.trim(rest, "{") |> String.trim_trailing("}") |> String.trim()
        if clean == "", do: nil, else: %{summary: clean, detail: nil}
    end
  end

  defp parse_empty_result(raw) do
    %{summary: "the agent produced no RESULT", detail: String.slice(raw, 0, 200)}
  end

  defp runs_dir! do
    case trusted_runs_dir() do
      {:ok, dir} -> dir
      _ -> ".runs"
    end
  end

  @doc "Decoded canonical event records for one run, uncapped by default."
  def events(run_id, opts \\ [])

  def events(run_id, opts) when is_binary(run_id) and is_list(opts) do
    if Mitos.Trace.valid_run_id?(run_id) do
      with {:ok, dir} <- trusted_run_dir(run_id) do
        events = read_jsonl(Path.join(dir, "events.jsonl"))
        {:ok, limit_events(events, Keyword.get(opts, :limit, :all))}
      else
        _ -> {:error, :not_found}
      end
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
    if Mitos.Trace.valid_run_id?(run_id) and regular_dir?(dir) do
      manifest = read_manifest(Path.join(dir, "run.yaml"))
      {event_count, last_event, status_event, title, subtitle} =
        scan_events(Path.join(dir, "events.jsonl"))

      approvals = count_lines(Path.join(dir, "approvals.jsonl"))

      %{
        "run_id" => run_id,
        "provider" => manifest["provider"],
        "task" => manifest["task"],
        "title" => title,
        "subtitle" => subtitle,
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
    case read_regular_file(path) do
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

        {title, subtitle} = title_from_events(decoded)

        {length(lines), List.last(decoded), status_source(decoded), title, subtitle}

      {:error, _} ->
        {0, nil, nil, nil, nil}
    end
  end

  # Derive a human title + subtitle ("app · entrypoint") from the run's
  # signal/task events so the list and detail pages don't show a bare run id.
  defp title_from_events(events) do
    signal = Enum.find(events, &(&1["type"] == "signal_received"))
    enqueue = Enum.find(events, &(&1["type"] == "task_enqueued"))

    ref = get_in(signal, ["ref"]) || %{}
    app_name = ref["app_name"] || get_in(enqueue, ["payload", "task", "provenance", "app_name"])
    entrypoint = ref["entrypoint"] || get_in(enqueue, ["payload", "task", "provenance", "entrypoint"])
    intent = get_in(signal, ["payload", "intake", "intent"]) || get_in(signal, ["payload", "intent"])
    pipeline = get_in(enqueue, ["payload", "task", "pipeline"])

    subtitle =
      [app_name, entrypoint]
      |> Enum.reject(&is_nil/1)
      |> Enum.join(" · ")

    title = intent || entrypoint || pipeline || nil

    title = if title == "", do: nil, else: title

    {title, if(subtitle == "", do: nil, else: subtitle)}
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
    case read_regular_file(path) do
      {:ok, raw} -> raw |> String.split("\n", trim: true) |> length()
      {:error, _} -> 0
    end
  end

  defp limit_events(events, :all), do: events

  defp limit_events(events, limit) when is_integer(limit) and limit > 0,
    do: Enum.take(events, -limit)

  defp limit_events(events, _limit), do: events

  defp read_jsonl(path) do
    case read_regular_file(path) do
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
    case read_regular_file(path) do
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

  defp regular_dir?(path), do: match?({:ok, %{type: :directory}}, File.lstat(path))

  defp trusted_runs_dir do
    with {:ok, base_dir} <- trusted_trace_base_dir(),
         runs_dir <- Path.join(base_dir, ".runs"),
         true <- regular_dir?(runs_dir) do
      {:ok, runs_dir}
    else
      _ -> {:error, :not_found}
    end
  end

  defp trusted_run_dir(run_id) do
    with {:ok, runs_dir} <- trusted_runs_dir(),
         dir <- Path.join(runs_dir, run_id),
         true <- regular_dir?(dir) do
      {:ok, dir}
    else
      _ -> {:error, :not_found}
    end
  end

  defp trusted_trace_base_dir do
    path = Path.expand(Mitos.Trace.base_dir())

    with true <- regular_dir?(path),
         true <- no_symlink_absolute_path?(path) do
      {:ok, path}
    else
      _ -> {:error, :not_found}
    end
  end

  defp no_symlink_absolute_path?(path) do
    root = Path.expand("/")
    path = Path.expand(path)
    parts = if path == root, do: [], else: Path.split(Path.relative_to(path, root))

    with {:ok, %{type: root_type}} <- File.lstat(root),
         true <- root_type != :symlink do
      parts
      |> Enum.reduce_while(root, fn part, current ->
        candidate = Path.join(current, part)

        case File.lstat(candidate) do
          {:ok, %{type: :symlink}} -> {:halt, false}
          {:ok, _stat} -> {:cont, candidate}
          {:error, _reason} -> {:halt, false}
        end
      end)
      |> case do
        false -> false
        _last_path -> true
      end
    else
      _ -> false
    end
  end

  defp read_regular_file(path) do
    case File.lstat(path) do
      {:ok, %{type: :regular}} -> File.read(path)
      {:ok, %{type: :symlink}} -> {:error, :symlink}
      {:ok, _stat} -> {:error, :not_regular}
      {:error, reason} -> {:error, reason}
    end
  end
end
