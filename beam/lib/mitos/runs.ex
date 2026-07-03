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

  @doc "The persisted inner trace of one stage (11 §4 inner zoom), or []."
  def trace(run_id, stage) do
    path = Path.join([Mitos.run_dir(run_id), "trace", stage <> ".jsonl"])

    case File.read(path) do
      {:ok, body} ->
        body
        |> String.split("\n", trim: true)
        |> Enum.flat_map(fn line ->
          case Mitos.JSON.decode(line) do
            {:ok, %{} = e} -> [e]
            _ -> []
          end
        end)

      _ ->
        []
    end
  end

  defp pipeline_name(run_id) do
    case Mitos.Run.Config.load(run_id) do
      {:ok, config} -> Mitos.Pipeline.parse(config.pipeline).name
      _ -> nil
    end
  end
end
