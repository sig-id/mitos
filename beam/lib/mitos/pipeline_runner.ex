defmodule Mitos.PipelineRunner do
  @moduledoc """
  Executes a workload-backed pipeline as ordered workload-capability steps.

  Each step names a concrete wrapper via `workflow.steps[].capability_id`. Step
  runs are normal workload runs, but their work-pool Task context is tagged with
  the parent pipeline id so the board shows a composed workflow instead of
  unrelated standalone wrapper calls.
  """

  @doc "Run a pipeline's declared steps in order."
  def execute(id, bindings, opts \\ [])

  def execute(id, bindings, opts) when is_binary(id) and is_map(bindings) do
    with {:ok, pipeline} <- Mitos.PipelineRegistry.get(id) do
      execute_definition(pipeline, bindings, opts)
    end
  end

  def execute(_id, _bindings, _opts),
    do: {:error, {:invalid_pipeline, "pipeline id is required"}}

  @doc "Run an already loaded pipeline definition."
  def execute_definition(pipeline, bindings, opts \\ [])

  def execute_definition(%{} = pipeline, bindings, opts) when is_map(bindings) do
    execution_id =
      Keyword.get(opts, :pipeline_execution_id) ||
        "pipe-" <> Base.url_encode64(:crypto.strong_rand_bytes(8), padding: false)

    with {:ok, steps} <- pipeline_steps(pipeline),
         {:ok, results} <-
           run_steps(
             pipeline,
             steps,
             bindings,
             opts
             |> Keyword.put(:pipeline_execution_id, execution_id)
             |> Keyword.put(:pipeline_task_prefix, Keyword.get(opts, :task_id) || execution_id)
           ) do
      {:ok, response(pipeline, results, execution_id)}
    end
  end

  def execute_definition(_pipeline, _bindings, _opts),
    do: {:error, {:invalid_pipeline, "pipeline definition must be an object"}}

  defp pipeline_steps(%{"workflow" => %{"steps" => steps}}) when is_list(steps) do
    steps =
      steps
      |> Enum.with_index(1)
      |> Enum.flat_map(fn
        {%{"capability_id" => capability_id} = step, index}
        when is_binary(capability_id) and capability_id != "" ->
          [
            %{
              "id" => string_or(step["id"], "step-#{index}"),
              "name" => string_or(step["name"], capability_id),
              "capability_id" => capability_id,
              "notes" => string_or(step["notes"], nil),
              "index" => index
            }
          ]

        _other ->
          []
      end)

    case steps do
      [] -> {:error, {:invalid_pipeline, "pipeline has no executable steps"}}
      steps -> {:ok, steps}
    end
  end

  defp pipeline_steps(_pipeline),
    do: {:error, {:invalid_pipeline, "pipeline has no executable steps"}}

  defp run_steps(pipeline, steps, bindings, opts) do
    Enum.reduce_while(steps, {:ok, []}, fn step, {:ok, results} ->
      with {:ok, workload} <- step_workload(step, opts),
           {:ok, result} <-
             Mitos.Workload.Runner.execute(
               workload,
               step_bindings(bindings, step, results),
               Keyword.merge(opts,
                 task_id: step_task_id(pipeline, step, opts),
                 pool: Keyword.get(opts, :pool, "interactive"),
                 source: "pipeline:#{pipeline["id"]}:#{step["id"]}",
                 pipeline: pipeline["pipeline_id"] || "workload:#{pipeline["id"]}",
                 provenance: Keyword.get(opts, :provenance, %{})
               )
             ) do
        {:cont, {:ok, results ++ [step_result(step, workload, result)]}}
      else
        {:error, reason} ->
          {:halt, {:error, {:pipeline_step_failed, step["id"], step["capability_id"], reason}}}
      end
    end)
  end

  defp step_workload(step, opts) do
    resolver = Keyword.get(opts, :capability_resolver, &Mitos.Workload.fetch/1)
    resolver.(step["capability_id"])
  end

  defp step_bindings(bindings, step, previous_results) do
    bindings
    |> Map.put("pipeline_step", step["name"])
    |> Map.put("pipeline_step_id", step["id"])
    |> Map.put("previous_results", previous_results_summary(previous_results))
  end

  defp previous_results_summary([]), do: ""

  defp previous_results_summary(results) do
    results
    |> Enum.map(fn result -> "#{result["name"]}: #{result["summary"]}" end)
    |> Enum.join("\n")
  end

  defp step_task_id(pipeline, step, opts) do
    prefix =
      Keyword.get(opts, :pipeline_task_prefix) ||
        Keyword.get(opts, :task_id) ||
        "pipeline-#{pipeline["id"]}"

    "#{prefix}:#{step["id"]}"
  end

  defp step_result(step, workload, result) do
    proof = Mitos.Proof.build(result.run_id)
    delivery_result = get_in(proof, ["delivery", "result"]) || %{}

    %{
      "id" => step["id"],
      "name" => step["name"],
      "capability_id" => workload.id,
      "run_id" => result.run_id,
      "status" => "completed",
      "summary" => delivered_summary(delivery_result, result.result),
      "exit_code" => result.exit_code,
      "errors" => result.errors,
      "proof" => proof
    }
  end

  defp delivered_summary(%{"synthetic" => true}, fallback), do: fallback

  defp delivered_summary(%{} = delivery_result, fallback) do
    needs = List.wrap(delivery_result["needs"])

    if Enum.any?(needs, &(&1 in ["result_missing", "result_invalid"])) do
      fallback
    else
      delivery_result["summary"] || delivery_result["signal"] || fallback
    end
  end

  defp response(pipeline, results, execution_id) do
    summary =
      results
      |> List.last()
      |> case do
        nil -> "pipeline completed"
        result -> result["summary"]
      end

    %{
      "pipeline_id" => pipeline["id"],
      "execution_id" => execution_id,
      "status" => "completed",
      "deliverable_summary" => summary,
      "steps" => results,
      "evidence" => %{
        "run_ids" => Enum.map(results, & &1["run_id"]),
        "steps" =>
          Enum.map(results, fn result ->
            Map.take(result, ["id", "name", "capability_id", "run_id", "status", "summary"])
          end)
      }
    }
  end

  defp string_or(value, _default) when is_binary(value) and value != "", do: value
  defp string_or(_value, default), do: default
end
