defmodule Mitos.Runs do
  @moduledoc """
  Run-history read model: enumerate a tenant's runs off durable state alone —
  each row is a fold of that run's log (01 §4; the frontend never invents
  state, 11 §1). Works with zero live processes; the log directory is the
  index.
  """

  alias Mitos.Run.State

  # Bound the raw-line scan in trace/3. High enough that a normal trace is
  # never cut by it (the event-level take is the real bound); low enough that a
  # runaway file is bounded in time.
  @raw_scan_cap 200_000

  @doc "All runs on disk, newest first: `%{run_id, status, started, spent, stages, org}`."
  def list do
    base = Path.join(Mitos.data_dir(), "runs")

    base
    |> Path.join("*/*/events.jsonl")
    |> Path.wildcard()
    |> Enum.map(fn events_path ->
      events_path |> Path.dirname() |> Path.relative_to(base)
    end)
    |> Enum.map(&summary/1)
    |> Enum.sort_by(& &1.started, :desc)
  end

  @doc "One run's summary row."
  def summary(run_id) do
    state = State.fold(run_id)
    first = List.first(state.events)

    %{
      run_id: run_id,
      status: state.status,
      started: (first && first.ts) || "",
      spent: state.spent,
      stages: Enum.map(state.stage_order, &{&1, state.stages[&1].status}),
      org: run_id |> String.split("/") |> hd(),
      pipeline: pipeline_name(run_id)
    }
  end

  @doc """
  The persisted inner trace of one stage (11 §4 inner zoom).

  Returns `%{events: [...], total_shown: n, truncated: bool, bytes: size}` — the
  trace is STREAMED and bounded (`limit`, default 2_000 events) so a runaway
  trace (e.g. a harness that flooded stderr) can't OOM the reader. `truncated`
  is true when more than `limit` events exist on disk. The full file is never
  read into memory.
  """
  def trace(run_id, stage, opts \\ []) do
    path = Path.join([Mitos.run_dir(run_id), "trace", stage <> ".jsonl"])
    limit = Keyword.get(opts, :limit, 2_000)

    case File.stat(path) do
      {:ok, %{:size => bytes}} when bytes > 0 ->
        # take limit+1 real events so truncation is detectable without reading
        # past it. `decode_event/1` also heals legacy traces — pre-fix adapters
        # misfiled unrecognized harness JSON (e.g. pi thinking/message frames)
        # as :stderr, so a stderr line whose payload is itself JSON is dropped
        # and only genuine non-JSON stderr remains. `@raw_scan_cap` bounds the
        # line scan so a pathologically huge file can't pin the reader even
        # when almost every line is dropped.
        legacy = legacy_trace?(path)

        # Decode the bounded raw scan in full, THEN coalesce per-token text
        # runs, THEN apply the display limit. Coalescing before the limit is
        # what makes a trace whose raw stream is dominated by per-token
        # thinking deltas render as whole turns instead of stopping partway
        # through turn one. Memory is bounded by the decoded event count (the
        # raw JSON blobs are discarded once decoded), not by file size.
        coalesced =
          path
          |> File.stream!()
          |> Stream.map(&String.trim_trailing(&1, "\n"))
          |> Stream.filter(&(&1 != ""))
          |> Stream.take(@raw_scan_cap)
          |> Stream.flat_map(&decode_event(&1, legacy))
          |> Enum.to_list()
          |> coalesce_text()

        truncated = length(coalesced) > limit
        events = Enum.take(coalesced, limit)

        %{events: events, total_shown: length(events), truncated: truncated, bytes: bytes}

      _ ->
        %{events: [], total_shown: 0, truncated: false, bytes: 0}
    end
  end

  defp decode_event(line, legacy?) do
    case Mitos.JSON.decode(line) do
      {:ok, %{"type" => "stderr", "line" => inner} = e} ->
        case Mitos.JSON.decode(inner) do
          # Re-hydrate a misfiled raw pi frame through the fixed adapter so
          # tools/results/usage/reasoning are recovered.
          {:ok, %{} = frame} -> Enum.map(Mitos.Harness.Pi.normalize_frame(frame), &stringify/1)
          # Genuine non-JSON stderr (stack traces, auth errors) stays.
          _ -> [e]
        end

      # Legacy mode: these were emitted by the old buggy adapter and are
      # recovered (and corrected) from the re-hydrated frames above.
      {:ok, %{"type" => type}} when legacy? and type in ["assistant_text", "token_usage", "final_result"] ->
        []

      {:ok, %{} = e} ->
        [e]

      _ ->
        []
    end
  end

  # A legacy trace carries raw harness JSON inside :stderr blobs: the signature
  # of the pre-fix pi adapter. Bounded head scan (blobs appear within the first
  # few frames of any real run) so this is cheap on a multi-hundred-MB file.
  defp legacy_trace?(path) do
    path
    |> File.stream!()
    |> Stream.map(&String.trim_trailing(&1, "\n"))
    |> Stream.filter(&(&1 != ""))
    |> Stream.take(3_000)
    |> Enum.any?(fn line ->
      with {:ok, %{"type" => "stderr", "line" => inner}} <- Mitos.JSON.decode(line),
           {:ok, %{}} <- Mitos.JSON.decode(inner) do
        true
      else
        _ -> false
      end
    end)
  end

  # pi.normalize_frame returns atom-keyed maps (write-side contract); the rest
  # of the trace path speaks string keys (events round-trip through JSONL), so
  # stringify the top-level keys of re-hydrated events. The :type value is an
  # atom there too, so coerce just that (leave e.g. `thinking: true` alone —
  # booleans are atoms in Erlang and must stay booleans).
  defp stringify(%{} = m) do
    Map.new(m, fn
      {:type, v} -> {"type", to_string(v)}
      {k, v} -> {to_string(k), v}
    end)
  end

  # Fold runs of consecutive assistant_text deltas (pi streams one event per
  # token) into a single block, so the page isn't a wall of one-word entries.
  # Reasoning deltas carry `thinking: true` (pi.ex); a thinking run and an
  # answer run never merge even when adjacent.
  defp coalesce_text(events) do
    events
    |> Enum.reduce([], fn
      %{"type" => "assistant_text"} = e, [%{"type" => "assistant_text"} = prev | rest] ->
        if Map.get(prev, "thinking") == Map.get(e, "thinking") do
          [%{prev | "text" => (prev["text"] || "") <> (e["text"] || "")} | rest]
        else
          [e, prev | rest]
        end

      %{"type" => "assistant_text"} = e, acc ->
        [e | acc]

      e, acc ->
        [e | acc]
    end)
    |> Enum.reverse()
  end

  defp pipeline_name(run_id) do
    case Mitos.Run.Config.load(run_id) do
      {:ok, config} -> Mitos.Pipeline.parse(config.pipeline).name
      _ -> nil
    end
  end
end
