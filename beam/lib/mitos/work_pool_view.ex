defmodule Mitos.WorkPoolView do
  @moduledoc """
  Read-side UI projection for pipelines and the durable work pool.

  The canonical source remains the Effect log. This module folds those events
  into board-ready columns and augments workload definitions with live task
  counts so the browser does not need to reverse-engineer the scheduler state.
  """

  @statuses ~w(blocked queued scheduled running parked done failed cancelled)
  @active_statuses ~w(blocked queued scheduled running parked)

  @doc "Return a board-ready work-pool projection across all known runs."
  def board do
    events = Mitos.Runs.all_events()
    state = Mitos.WorkPool.fold_events(events)
    tasks = materialized_tasks(state)

    %{
      "statuses" => @statuses,
      "columns" => columns(tasks),
      "tasks" => tasks,
      "pools" => pool_summaries(state, tasks),
      "ready_tasks" => state["ready_tasks"] || [],
      "blocked_tasks" => state["blocked_tasks"] || [],
      "invocations" => state["invocations"] || %{},
      "cycles" => state["cycles"] || [],
      "totals" => totals(tasks),
      "run_count" => length(Mitos.Runs.list())
    }
  end

  @doc "Return public pipeline records derived from packaged workloads."
  def pipelines do
    board = board()
    tasks = board["tasks"] || []

    pipelines =
      Mitos.Workload.list()
      |> Enum.map(&pipeline_view(&1, tasks))

    %{
      "pipelines" => pipelines,
      "totals" => %{
        "pipelines" => length(pipelines),
        "active" => Enum.count(pipelines, &(get_in(&1, ["tasks", "active"]) > 0)),
        "paid" => Enum.count(pipelines, &Map.has_key?(&1, "pricing"))
      }
    }
  end

  @doc "Return one public pipeline record with task counts."
  def pipeline(id) when is_binary(id) do
    board = board()
    tasks = board["tasks"] || []

    with {:ok, workload} <- Mitos.Workload.fetch(id) do
      {:ok, pipeline_view(workload, tasks)}
    end
  end

  def pipeline(_id), do: {:error, :not_found}

  defp materialized_tasks(state) do
    blocked = MapSet.new(state["blocked_tasks"] || [])

    state
    |> Map.get("tasks", %{})
    |> Map.values()
    |> Enum.map(&task_view(&1, blocked))
    |> Enum.sort_by(fn task ->
      {status_rank(task["status"]), -(task["priority"] || 0),
       get_in(task, ["ts", "created"]) || ""}
    end)
  end

  defp task_view(task, blocked) do
    status =
      if task["id"] in blocked and task["status"] in ["queued", "blocked"] do
        "blocked"
      else
        task["status"]
      end

    %{
      "id" => task["id"],
      "status" => status,
      "pool" => task["pool"],
      "pipeline" => task["pipeline"],
      "source" => task["source"],
      "priority" => task["priority"],
      "tenant" => task["tenant"] || %{},
      "deps" => task["deps"] || [],
      "join" => task["join"],
      "runs" => task["runs"] || [],
      "budget" => task["budget"] || %{},
      "payload" => task_payload(task["payload"] || %{}),
      "provenance" => task["provenance"] || %{},
      "invoke" => task["invoke"],
      "external" => task["external"],
      "result" => task["result"],
      "status_reason" => task["status_reason"] || task["parked_reason"],
      "invoke_waiting_for" => task["invoke_waiting_for"],
      "last_subtask_result" => task["last_subtask_result"],
      "timestamps" => task["ts"] || %{},
      "created_at" => get_in(task, ["ts", "created"]),
      "updated_at" => get_in(task, ["ts", "updated"])
    }
  end

  defp task_payload(payload) do
    payload
    |> Map.take([
      "workload_id",
      "intent",
      "inputs",
      "dedupe_key",
      "reply_ref",
      "dependency_results"
    ])
  end

  defp columns(tasks) do
    Map.new(@statuses, fn status ->
      {status, Enum.filter(tasks, &(&1["status"] == status))}
    end)
  end

  defp pool_summaries(state, tasks) do
    pool_ids =
      ((state["pools"] || %{}) |> Map.keys()) ++ Enum.map(tasks, & &1["pool"])

    pool_ids
    |> Enum.reject(&is_nil/1)
    |> Enum.uniq()
    |> Enum.sort()
    |> Enum.map(fn pool_id ->
      pool_tasks = Enum.filter(tasks, &(&1["pool"] == pool_id))
      pool = get_in(state, ["pools", pool_id]) || %{"id" => pool_id}

      %{
        "id" => pool_id,
        "discipline" => pool["discipline"] || "fifo",
        "concurrency" => pool["concurrency"],
        "tasks" => length(pool_tasks),
        "active" => Enum.count(pool_tasks, &(&1["status"] in @active_statuses)),
        "done" => Enum.count(pool_tasks, &(&1["status"] == "done")),
        "failed" => Enum.count(pool_tasks, &(&1["status"] == "failed"))
      }
    end)
  end

  defp totals(tasks) do
    base = Map.new(@statuses, &{&1, 0})

    tasks
    |> Enum.reduce(base, fn task, acc -> Map.update(acc, task["status"], 1, &(&1 + 1)) end)
    |> Map.put("all", length(tasks))
    |> Map.put("active", Enum.count(tasks, &(&1["status"] in @active_statuses)))
  end

  defp pipeline_view(workload, tasks) do
    public = Mitos.Workload.public_view(workload)
    pipeline_id = "workload:#{workload.id}"
    pipeline_tasks = Enum.filter(tasks, &(&1["pipeline"] == pipeline_id))
    workflow = persisted_workflow(workload.id)
    executions = pipeline_executions(pipeline_tasks, workflow)
    active = Enum.count(pipeline_tasks, &(&1["status"] in @active_statuses))

    public
    |> Map.put("pipeline_id", pipeline_id)
    |> Map.put("kind", "workload")
    |> maybe_put("workflow", workflow)
    |> Map.put("tasks", %{
      "total" => length(pipeline_tasks),
      "active" => active,
      "done" => Enum.count(pipeline_tasks, &(&1["status"] == "done")),
      "failed" => Enum.count(pipeline_tasks, &(&1["status"] == "failed"))
    })
    |> Map.put("executions", executions)
    |> Map.put(
      "recent_runs",
      executions
      |> Enum.flat_map(&(&1["runs"] || []))
      |> Enum.uniq()
      |> Enum.take(5)
    )
  end

  defp pipeline_executions([], _workflow), do: []

  defp pipeline_executions(tasks, workflow) do
    step_index = workflow_step_index(workflow)

    tasks
    |> Enum.group_by(&execution_id/1)
    |> Enum.map(fn {id, grouped_tasks} ->
      grouped_tasks = Enum.sort_by(grouped_tasks, &step_sort_key(&1, step_index))
      statuses = Enum.map(grouped_tasks, & &1["status"])
      runs = grouped_tasks |> Enum.flat_map(&(&1["runs"] || [])) |> Enum.uniq()

      %{
        "id" => id,
        "status" => execution_status(statuses),
        "task_count" => length(grouped_tasks),
        "counts" => %{
          "total" => length(grouped_tasks),
          "active" => Enum.count(statuses, &(&1 in @active_statuses)),
          "done" => Enum.count(statuses, &(&1 == "done")),
          "failed" => Enum.count(statuses, &(&1 == "failed"))
        },
        "runs" => runs,
        "steps" => Enum.map(grouped_tasks, &execution_step(&1, step_index)),
        "created_at" => grouped_tasks |> Enum.map(& &1["created_at"]) |> compact_min(),
        "updated_at" => grouped_tasks |> Enum.map(& &1["updated_at"]) |> compact_max()
      }
    end)
    |> Enum.sort_by(
      fn execution ->
        execution["updated_at"] || execution["created_at"] || execution["id"]
      end,
      :desc
    )
  end

  defp workflow_step_index(%{"steps" => steps}) when is_list(steps) do
    steps
    |> Enum.with_index(1)
    |> Enum.reduce(%{}, fn {step, index}, acc ->
      step_id = string_or(step["id"], step["capability_id"])

      if is_binary(step_id) and step_id != "" do
        Map.put(acc, step_id, %{
          "index" => index,
          "id" => step_id,
          "name" => string_or(step["name"], step_id),
          "capability_id" => string_or(step["capability_id"], nil)
        })
      else
        acc
      end
    end)
  end

  defp workflow_step_index(_workflow), do: %{}

  defp execution_id(%{"id" => id}) when is_binary(id) do
    case String.split(id, ":", parts: 2) do
      [execution_id, _step_id] when execution_id != "" -> execution_id
      _ -> id
    end
  end

  defp execution_id(_task), do: "unknown"

  defp execution_status(statuses) do
    cond do
      Enum.any?(statuses, &(&1 == "failed")) -> "failed"
      Enum.any?(statuses, &(&1 == "cancelled")) -> "cancelled"
      Enum.any?(statuses, &(&1 == "blocked")) -> "blocked"
      Enum.any?(statuses, &(&1 == "running")) -> "running"
      Enum.any?(statuses, &(&1 == "parked")) -> "parked"
      Enum.any?(statuses, &(&1 == "scheduled")) -> "scheduled"
      Enum.any?(statuses, &(&1 == "queued")) -> "queued"
      statuses != [] and Enum.all?(statuses, &(&1 == "done")) -> "done"
      true -> "unknown"
    end
  end

  defp execution_step(task, step_index) do
    step_id = step_id(task)
    step = Map.get(step_index, step_id, %{})

    %{
      "id" => step_id,
      "name" => step["name"] || step_id,
      "capability_id" => step["capability_id"] || get_in(task, ["payload", "workload_id"]),
      "task_id" => task["id"],
      "status" => task["status"],
      "pool" => task["pool"],
      "runs" => task["runs"] || [],
      "source" => task["source"],
      "created_at" => task["created_at"],
      "updated_at" => task["updated_at"],
      "status_reason" => task["status_reason"]
    }
  end

  defp step_sort_key(task, step_index) do
    step_id = step_id(task)
    step = Map.get(step_index, step_id, %{})
    {step["index"] || 999, task["created_at"] || "", task["id"] || ""}
  end

  defp step_id(%{"id" => id}) when is_binary(id) do
    case String.split(id, ":", parts: 2) do
      [_execution_id, step_id] when step_id != "" -> step_id
      _ -> id
    end
  end

  defp step_id(_task), do: "task"

  defp compact_min(values) do
    values
    |> Enum.reject(&is_nil/1)
    |> Enum.min(fn -> nil end)
  end

  defp compact_max(values) do
    values
    |> Enum.reject(&is_nil/1)
    |> Enum.max(fn -> nil end)
  end

  defp string_or(value, _default) when is_binary(value) and value != "", do: value
  defp string_or(_value, default), do: default

  defp status_rank(status), do: Enum.find_index(@statuses, &(&1 == status)) || length(@statuses)

  defp persisted_workflow(id) do
    path = Path.join(Mitos.Workload.dir(), id <> ".json")

    with {:ok, raw} <- read_regular_file(path),
         {:ok, json} <- Jason.decode(strip_json_comments(raw)),
         %{} = workflow <- json["workflow"] do
      workflow
    else
      _ -> nil
    end
  end

  defp read_regular_file(path) do
    case File.lstat(path) do
      {:ok, %{type: :regular}} -> File.read(path)
      {:ok, _stat} -> {:error, :not_regular}
      {:error, reason} -> {:error, reason}
    end
  end

  defp strip_json_comments(raw) do
    raw
    |> String.split("\n")
    |> Enum.reject(&String.starts_with?(String.trim_leading(&1), "//"))
    |> Enum.join("\n")
  end

  defp maybe_put(map, _key, nil), do: map
  defp maybe_put(map, key, value), do: Map.put(map, key, value)
end
