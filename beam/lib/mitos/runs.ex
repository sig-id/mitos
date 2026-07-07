defmodule Mitos.Runs do
  @moduledoc """
  Run-history read model: enumerate a tenant's runs off durable state alone —
  each row is a fold of that run's log (01 §4; the frontend never invents
  state, 11 §1). Works with zero live processes; the log directory is the
  index.
  """

  alias Mitos.Run.State

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
        # take limit+1 so we can detect truncation without reading the whole file
        taken =
          path
          |> File.stream!()
          |> Stream.map(&String.trim_trailing(&1, "\n"))
          |> Stream.filter(&(&1 != ""))
          |> Stream.flat_map(fn line ->
            case Mitos.JSON.decode(line) do
              {:ok, %{} = e} -> [e]
              _ -> []
            end
          end)
          |> Stream.take(limit + 1)
          |> Enum.to_list()

        truncated = length(taken) > limit
        events = Enum.take(taken, limit)

        %{events: events, total_shown: length(events), truncated: truncated, bytes: bytes}

      _ ->
        %{events: [], total_shown: 0, truncated: false, bytes: 0}
    end
  end

  defp pipeline_name(run_id) do
    case Mitos.Run.Config.load(run_id) do
      {:ok, config} -> Mitos.Pipeline.parse(config.pipeline).name
      _ -> nil
    end
  end
end
