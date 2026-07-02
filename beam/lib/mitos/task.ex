defmodule Mitos.Task do
  @moduledoc """
  The work-pool unit (01 §8.7, elaborated 06). A durable unit of intent; 0..N
  Runs execute it. `task.status` (queue) is distinct from `run.status`
  (execution). Human backlog, connector signals, agent follow-ups, invoked
  subtasks, and paid jobs are all Tasks.
  """

  @enforce_keys [:id, :tenant]
  defstruct id: nil,
            # %{org, project}
            tenant: nil,
            # human | trigger:<id> | connector:<name> | run:<run_id> | purchase:<id>
            source: "human",
            pool: "batch",
            priority: 0,
            # queued | blocked | scheduled | running | done | failed | parked | cancelled
            status: :queued,
            deps: [],
            # pipeline_ref | nil (nil → dispatch selects)
            pipeline: nil,
            # %{intent, workspace?, inputs, parameters?, constraints, success_criteria}
            payload: %{},
            # %{tokens, usd, wall_ms}
            budget: %{},
            runs: [],
            provenance: %{created_by: "human", depth: 0, parent_task: nil},
            invoke: nil,
            external: nil,
            ts: %{}

  def new(fields) do
    fields = Map.new(fields)
    id = Map.get(fields, :id) || Mitos.id("task")
    struct!(__MODULE__, Map.put(fields, :id, id))
  end
end
