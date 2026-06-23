defmodule Mitos.WorkPool do
  @moduledoc """
  Event-log-backed work pool projection and selector.

  A pool is not a queue; it is a durable container of Tasks plus a discipline
  that decides which ready Task to drain next. This module owns the deterministic
  fold over Task lifecycle Effects and the non-agent selection disciplines.
  Dispatching the selected Task into a run remains a separate control-plane step.
  """

  alias Mitos.Trace

  @task_statuses ~w(queued blocked scheduled running done failed parked cancelled)
  @default_tenant %{"org" => "default", "project" => "default"}
  @default_budget %{"tokens" => 0, "usd" => 0, "wall_ms" => 0}

  @doc "Build a canonical Task map with spec defaults."
  def task(attrs) when is_map(attrs) do
    attrs = stringify(attrs)
    id = required_string!(attrs, "id")

    %{
      "id" => id,
      "tenant" => map_or(attrs["tenant"], @default_tenant),
      "source" => string_or(attrs["source"], "manual"),
      "pool" => string_or(attrs["pool"], "default"),
      "priority" => int_or(attrs["priority"], 0),
      "status" => status_or(attrs["status"], "queued"),
      "deps" => list_or(attrs["deps"]),
      "join" => join_or(attrs["join"] || attrs["join_rule"], %{"rule" => "all"}),
      "pipeline" => attrs["pipeline"],
      "payload" => map_or(attrs["payload"], %{}),
      "budget" => map_or(attrs["budget"], @default_budget),
      "runs" => list_or(attrs["runs"]),
      "provenance" => map_or(attrs["provenance"], %{}),
      "invoke" => map_or_nil(attrs["invoke"]),
      "external" => map_or_nil(attrs["external"]),
      "ts" => map_or(attrs["ts"], %{})
    }
  end

  @doc "Append a canonical `task_enqueued` Effect to a trace."
  def append_enqueue(%Trace{} = trace, task_attrs, opts)
      when is_map(task_attrs) and is_list(opts) do
    task = task(task_attrs)

    Trace.append_effect(trace, %{
      type: "task_enqueued",
      caused_by: Keyword.fetch!(opts, :caused_by),
      ref: %{"task_id" => task["id"], "pool" => task["pool"]},
      payload: %{"task" => task}
    })
  end

  @doc """
  Append a governed fire-and-forget Task emitted by a running capability.

  This is distinct from `append_invoke/4`: no parent is parked and no result is
  returned. Admission enforces a provenance depth cap, simple budget cap, and
  deterministic deduplication over prior emitted/enqueued Tasks.
  """
  def append_emitted_task(%Trace{} = trace, parent_task_id, task_attrs, opts)
      when is_binary(parent_task_id) and is_map(task_attrs) and is_list(opts) do
    caused_by = Keyword.fetch!(opts, :caused_by)
    events = Keyword.get_lazy(opts, :events, fn -> read_trace_events(trace) end)
    state = fold_events(events)
    parent = get_in(state, ["tasks", parent_task_id]) || %{}
    task = emitted_task(parent_task_id, parent, task_attrs, opts)
    dedupe_key = emitted_dedupe_key(task, opts)
    decision = emitted_admission(task, dedupe_key, events, opts)

    trace =
      Trace.append_effect(trace, %{
        type: "task_emitted",
        caused_by: caused_by,
        ref: %{
          "parent_task_id" => parent_task_id,
          "task_id" => task["id"],
          "dedupe_key" => dedupe_key,
          "admission" => decision["status"]
        },
        payload: %{
          "task" => task,
          "dedupe_key" => dedupe_key,
          "admission" => decision
        }
      })

    case decision["status"] do
      "accepted" -> {append_enqueue(trace, task, caused_by: caused_by), task}
      _rejected -> {trace, nil}
    end
  end

  @doc "Append a Task lifecycle status Effect to a trace."
  def append_status(%Trace{} = trace, task_id, status, opts)
      when is_binary(task_id) and is_list(opts) do
    status =
      status_or(status, nil) || raise ArgumentError, "invalid task status #{inspect(status)}"

    if status == "queued" do
      raise ArgumentError, "use append_enqueue/3 to enqueue a queued task"
    end

    Trace.append_effect(trace, %{
      type: status_effect(status),
      caused_by: Keyword.fetch!(opts, :caused_by),
      ref: %{"task_id" => task_id},
      payload:
        %{"status" => status}
        |> put_optional("reason", Keyword.get(opts, :reason))
        |> put_optional("run_id", Keyword.get(opts, :run_id))
        |> put_optional("result", Keyword.get(opts, :result))
    })
  end

  @doc "Append an invoke request and enqueue the child Task under the `calls` pool by default."
  def append_invoke(%Trace{} = trace, parent_task_id, child_task_attrs, opts)
      when is_binary(parent_task_id) and is_map(child_task_attrs) and is_list(opts) do
    caused_by = Keyword.fetch!(opts, :caused_by)
    events = Keyword.get_lazy(opts, :events, fn -> read_trace_events(trace) end)
    state = fold_events(events)
    parent = get_in(state, ["tasks", parent_task_id]) || %{}
    child_task = invoke_child_task(parent_task_id, parent, child_task_attrs, opts)
    dedupe_key = invoke_dedupe_key(child_task, opts)
    admission = invoke_admission(child_task, dedupe_key, events, opts)

    trace =
      Trace.append_effect(trace, %{
        type: "invoke_requested",
        caused_by: caused_by,
        ref: %{
          "parent_task" => parent_task_id,
          "child_task" => child_task["id"],
          "dedupe_key" => dedupe_key,
          "admission" => admission["status"]
        },
        payload: %{
          "task" => child_task,
          "return_to" => Keyword.get(opts, :return_to),
          "timeout_ms" => Keyword.get(opts, :timeout_ms),
          "dedupe_key" => dedupe_key,
          "admission" => admission
        }
      })

    case admission["status"] do
      "accepted" -> {trace, child_task, admission}
      _rejected -> {trace, nil, admission}
    end
  end

  @doc "Append a subtask return Effect."
  def append_subtask_return(%Trace{} = trace, parent_task_id, child_task_id, result, opts)
      when is_binary(parent_task_id) and is_binary(child_task_id) and is_list(opts) do
    Trace.append_effect(trace, %{
      type: "subtask_returned",
      caused_by: Keyword.fetch!(opts, :caused_by),
      ref: %{"parent_task" => parent_task_id, "child_task" => child_task_id},
      payload: %{"result" => stringify(result)}
    })
  end

  @doc "Append a subtask timeout Effect that resumes the parent with an error result."
  def append_subtask_timeout(%Trace{} = trace, parent_task_id, child_task_id, opts)
      when is_binary(parent_task_id) and is_binary(child_task_id) and is_list(opts) do
    timeout_ms = positive_int_or_nil(Keyword.get(opts, :timeout_ms))
    result = Keyword.get(opts, :result) || timeout_result(timeout_ms)

    Trace.append_effect(trace, %{
      type: "subtask_timed_out",
      caused_by: Keyword.fetch!(opts, :caused_by),
      ref: %{"parent_task" => parent_task_id, "child_task" => child_task_id},
      payload:
        %{"result" => stringify(result)}
        |> put_optional("timeout_ms", timeout_ms)
    })
  end

  @doc "Return requested invocations whose timeout has elapsed at `now`."
  def expired_invokes(state, now \\ System.system_time(:millisecond)) when is_map(state) do
    now_ms = timestamp_ms(now)

    state
    |> Map.get("invocations", %{})
    |> Map.values()
    |> Enum.filter(&invoke_expired?(&1, now_ms))
  end

  @doc "Append timeout Effects for every expired requested invocation in the trace."
  def append_expired_timeouts(%Trace{} = trace, opts)
      when is_list(opts) do
    caused_by = Keyword.fetch!(opts, :caused_by)
    now = Keyword.get(opts, :now, Keyword.get(opts, :now_ms, System.system_time(:millisecond)))
    events = Keyword.get_lazy(opts, :events, fn -> read_trace_events(trace) end)
    state = fold_events(events)

    expired_invokes(state, now)
    |> Enum.reduce({trace, []}, fn invocation, {trace, timed_out} ->
      timeout_ms = positive_int_or_nil(invocation["timeout_ms"])

      trace =
        append_subtask_timeout(
          trace,
          invocation["parent_task"],
          invocation["child_task"],
          caused_by: caused_by,
          timeout_ms: timeout_ms
        )

      {trace, [invocation | timed_out]}
    end)
    |> then(fn {trace, timed_out} -> {trace, Enum.reverse(timed_out)} end)
  end

  @doc "Fold decoded trace events into Work Pool state."
  def fold_events(events) when is_list(events) do
    events
    |> Enum.flat_map(&canonical_effect/1)
    |> Enum.sort_by(&Map.get(&1, "seq", -1))
    |> Enum.reduce(initial_state(), &apply_effect/2)
    |> materialize_data_dependencies()
    |> finalize_ready_sets()
  end

  @doc "Read one run's events and fold Task lifecycle state."
  def fold_run(run_id, opts \\ []) when is_binary(run_id) do
    run_id
    |> Trace.read_events(opts)
    |> fold_events()
  end

  @doc "Return ready queued Tasks, optionally scoped to one pool id."
  def ready_tasks(state, pool_id \\ nil) when is_map(state) do
    state
    |> Map.get("tasks", %{})
    |> Map.values()
    |> Enum.filter(&ready_task?(&1, state))
    |> filter_pool(pool_id)
  end

  @doc "Select the next ready Task for a pool using its configured discipline."
  def select_task(state, pool, opts \\ []) when is_map(state) do
    with :ok <- validate_acyclic(state) do
      pool = pool_config(pool)

      candidates =
        state
        |> ready_tasks(pool["id"])
        |> filter_scope(pool)
        |> reject_conflicts(state)
        |> reject_tenant_capacity(state, pool)

      case {pool["discipline"], candidates} do
        {_discipline, []} -> :empty
        {"agent_pick", _candidates} -> agent_pick(candidates, opts)
        {discipline, candidates} -> {:ok, pick(discipline, candidates, state, pool)}
      end
    end
  end

  @doc """
  Drain one ready Task from a pool and hand it to `dispatch_fun`.

  The work pool owns deterministic selection and lifecycle Effects. The caller
  owns the actual dispatch policy: pipeline, capability, or internal service.
  """
  def drain_once(%Trace{} = trace, pool, dispatch_fun, opts \\ [])
      when is_function(dispatch_fun, 1) and is_list(opts) do
    caused_by = Keyword.fetch!(opts, :caused_by)
    events = Keyword.get_lazy(opts, :events, fn -> read_trace_events(trace) end)
    state = fold_events(events)

    case select_task(state, pool, selector: Keyword.get(opts, :selector)) do
      :empty ->
        {:empty, trace}

      {:error, reason} ->
        {:error, reason, trace}

      {:ok, task} ->
        dispatch_task(trace, task, dispatch_fun, caused_by)
    end
  end

  @doc "Validate that hard enabling dependency edges form a DAG."
  def validate_acyclic(state) when is_map(state) do
    case dependency_cycle(state) do
      nil -> :ok
      cycle -> {:error, {:dependency_cycle, cycle}}
    end
  end

  defp dispatch_task(trace, task, dispatch_fun, caused_by) do
    task_id = task["id"]
    run_id = trace.run_id

    trace =
      trace
      |> append_status(task_id, :scheduled, caused_by: caused_by)
      |> append_status(task_id, :running, caused_by: caused_by)

    case safe_dispatch(dispatch_fun, task) do
      {:ok, result} ->
        run_id = dispatch_run_id(result, run_id)

        trace =
          append_status(trace, task_id, :done,
            caused_by: caused_by,
            run_id: run_id,
            result: result
          )
          |> append_service_outcome(task, "succeeded", run_id, result, caused_by)

        {:ok, task, result, trace}

      {:error, reason} ->
        trace =
          append_status(trace, task_id, :failed,
            caused_by: caused_by,
            run_id: run_id,
            reason: reason_text(reason)
          )
          |> append_service_outcome(
            task,
            "failed",
            run_id,
            %{"reason" => reason_text(reason)},
            caused_by
          )

        {:error, task, reason, trace}
    end
  end

  defp append_service_outcome(%Trace{} = trace, task, status, run_id, detail, caused_by) do
    case task_service_id(task) do
      service_id when is_binary(service_id) and service_id != "" ->
        payload =
          %{
            "service_id" => service_id,
            "task_id" => task["id"],
            "run_id" => run_id,
            "status" => status,
            "detail" => stringify(detail)
          }

        trace
        |> Trace.append_effect(%{
          type: "service.#{status}:#{service_id}",
          caused_by: caused_by,
          ref: %{"service_id" => service_id, "task_id" => task["id"]},
          payload: payload
        })
        |> Trace.append_effect(%{
          type: "service.finished:#{service_id}",
          caused_by: caused_by,
          ref: %{"service_id" => service_id, "task_id" => task["id"]},
          payload: payload
        })

      _none ->
        trace
    end
  end

  defp task_service_id(task) do
    get_in(task, ["payload", "service_id"]) ||
      get_in(task, ["provenance", "service_id"]) ||
      service_id_from_source(task["source"])
  end

  defp service_id_from_source("service:" <> rest) do
    rest
    |> String.split(":", parts: 2)
    |> List.first()
  end

  defp service_id_from_source(_source), do: nil

  defp safe_dispatch(dispatch_fun, task) do
    task
    |> dispatch_fun.()
    |> normalize_dispatch_result()
  rescue
    exception -> {:error, Exception.message(exception)}
  catch
    kind, reason -> {:error, {kind, reason}}
  end

  defp normalize_dispatch_result({:ok, result}), do: {:ok, stringify(result)}
  defp normalize_dispatch_result(:ok), do: {:ok, %{}}
  defp normalize_dispatch_result({:error, reason}), do: {:error, reason}
  defp normalize_dispatch_result(%{} = result), do: {:ok, stringify(result)}
  defp normalize_dispatch_result(result), do: {:ok, %{"result" => result}}

  defp dispatch_run_id(%{"run_id" => run_id}, _fallback) when is_binary(run_id) and run_id != "",
    do: run_id

  defp dispatch_run_id(_result, fallback), do: fallback

  defp read_trace_events(%Trace{} = trace) do
    trace.run_id
    |> Trace.read_events(base_dir: trace_base_dir(trace))
  end

  defp trace_base_dir(%Trace{dir: dir}) do
    dir
    |> Path.dirname()
    |> Path.dirname()
  rescue
    _ -> Trace.base_dir()
  end

  defp reason_text(reason) when is_binary(reason), do: reason
  defp reason_text(reason), do: inspect(reason)

  defp emitted_task(parent_task_id, parent, task_attrs, opts) do
    parent_provenance = map_or(parent["provenance"], %{})
    parent_depth = int_or(parent_provenance["depth"], 0)
    attrs = stringify(task_attrs)
    provenance = map_or(attrs["provenance"], %{})

    attrs
    |> Map.put_new("pool", Keyword.get(opts, :pool, "batch"))
    |> Map.put_new("source", "emitted:#{parent_task_id}")
    |> Map.put_new("tenant", Map.get(parent, "tenant", @default_tenant))
    |> Map.put(
      "provenance",
      provenance
      |> Map.put("parent_task_id", parent_task_id)
      |> Map.put("emitted_by", Keyword.get(opts, :emitted_by, "capability"))
      |> Map.put("depth", parent_depth + 1)
    )
    |> task()
  end

  defp emitted_admission(task, dedupe_key, events, opts) do
    cond do
      depth_exceeded?(task, opts) ->
        %{"status" => "rejected", "reason" => "depth_exceeded", "max_depth" => max_depth(opts)}

      budget_exceeded?(task, events, opts) ->
        %{
          "status" => "rejected",
          "reason" => "budget_exceeded",
          "budget_cap" => stringify(Keyword.get(opts, :budget_cap, %{})),
          "budget_usage" => governed_budget_usage(task, events)
        }

      duplicate_emitted_task?(dedupe_key, events) ->
        %{"status" => "deduplicated", "reason" => "duplicate", "dedupe_key" => dedupe_key}

      true ->
        %{"status" => "accepted", "dedupe_key" => dedupe_key}
    end
  end

  defp emitted_dedupe_key(task, opts) do
    Keyword.get(opts, :dedupe_key) ||
      get_in(task, ["external", "ref"]) ||
      get_in(task, ["payload", "dedupe_key"]) ||
      "sha256:" <>
        (:crypto.hash(
           :sha256,
           canonical_json(%{
             "tenant" => task["tenant"],
             "pool" => task["pool"],
             "pipeline" => task["pipeline"],
             "payload" => task["payload"]
           })
         )
         |> Base.encode16(case: :lower))
  end

  defp duplicate_emitted_task?(nil, _events), do: false

  defp duplicate_emitted_task?(dedupe_key, events) do
    events
    |> Enum.flat_map(&canonical_effect/1)
    |> Enum.any?(fn
      %{
        "type" => "task_emitted",
        "payload" => %{"dedupe_key" => ^dedupe_key, "admission" => %{"status" => "accepted"}}
      } ->
        true

      %{
        "type" => "task_enqueued",
        "payload" => %{"task" => %{"payload" => %{"dedupe_key" => ^dedupe_key}}}
      } ->
        true

      _effect ->
        false
    end)
  end

  defp depth_exceeded?(task, opts) do
    max_depth = max_depth(opts)
    depth = get_in(task, ["provenance", "depth"]) || 0
    is_integer(max_depth) and depth > max_depth
  end

  defp max_depth(opts), do: Keyword.get(opts, :max_depth, 3)

  defp budget_exceeded?(task, events, opts) do
    cap = stringify(Keyword.get(opts, :budget_cap, %{}))
    usage = governed_budget_usage(task, events)

    Enum.any?(["tokens", "usd", "wall_ms"], fn key ->
      cap_value = numeric(cap[key])
      cap_value > 0 and numeric(usage[key]) > cap_value
    end)
  end

  defp governed_budget_usage(task, events) do
    task_tenant_key = tenant_key(task["tenant"])

    existing =
      events
      |> Enum.flat_map(&canonical_effect/1)
      |> Enum.flat_map(fn
        %{
          "type" => "task_emitted",
          "payload" => %{
            "admission" => %{"status" => "accepted"},
            "task" => %{"tenant" => tenant}
          }
        } = effect ->
          if tenant_key(tenant) == task_tenant_key do
            [get_in(effect, ["payload", "task", "budget"])]
          else
            []
          end

        %{
          "type" => "invoke_requested",
          "payload" => %{
            "admission" => %{"status" => "accepted"},
            "task" => %{"tenant" => tenant}
          }
        } = effect ->
          if tenant_key(tenant) == task_tenant_key do
            [get_in(effect, ["payload", "task", "budget"])]
          else
            []
          end

        _effect ->
          []
      end)

    [task["budget"] | existing]
    |> Enum.reduce(%{"tokens" => 0.0, "usd" => 0.0, "wall_ms" => 0.0}, fn budget, acc ->
      budget = map_or(budget, %{})

      %{
        "tokens" => acc["tokens"] + numeric(budget["tokens"]),
        "usd" => acc["usd"] + numeric(budget["usd"]),
        "wall_ms" => acc["wall_ms"] + numeric(budget["wall_ms"])
      }
    end)
  end

  defp invoke_child_task(parent_task_id, parent, child_task_attrs, opts) do
    parent_provenance = map_or(parent["provenance"], %{})
    parent_depth = int_or(parent_provenance["depth"], 0)
    attrs = stringify(child_task_attrs)
    provenance = map_or(attrs["provenance"], %{})

    attrs
    |> Map.put_new("pool", "calls")
    |> Map.put("source", "invoke:#{parent_task_id}")
    |> Map.put_new("tenant", Map.get(parent, "tenant", @default_tenant))
    |> Map.put(
      "provenance",
      provenance
      |> Map.put("parent_task_id", parent_task_id)
      |> Map.put("invoked_by", Keyword.get(opts, :invoked_by, "capability"))
      |> Map.put("depth", parent_depth + 1)
    )
    |> Map.put("invoke", %{
      "parent_task" => parent_task_id,
      "return_to" => Keyword.get(opts, :return_to)
    })
    |> task()
  end

  defp invoke_admission(task, dedupe_key, events, opts) do
    cond do
      depth_exceeded?(task, opts) ->
        %{"status" => "rejected", "reason" => "depth_exceeded", "max_depth" => max_depth(opts)}

      not invoke_allowed?(task, opts) ->
        %{
          "status" => "rejected",
          "reason" => "not_allowed",
          "capability_ref" => task_capability_ref(task)
        }

      budget_exceeded?(task, events, opts) ->
        %{
          "status" => "rejected",
          "reason" => "budget_exceeded",
          "budget_cap" => stringify(Keyword.get(opts, :budget_cap, %{})),
          "budget_usage" => governed_budget_usage(task, events)
        }

      duplicate_invoke?(dedupe_key, events) ->
        %{"status" => "deduplicated", "reason" => "duplicate", "dedupe_key" => dedupe_key}

      true ->
        %{"status" => "accepted", "dedupe_key" => dedupe_key}
    end
  end

  defp invoke_dedupe_key(task, opts) do
    Keyword.get(opts, :dedupe_key) ||
      get_in(task, ["external", "ref"]) ||
      get_in(task, ["payload", "dedupe_key"]) ||
      "sha256:" <>
        (:crypto.hash(
           :sha256,
           canonical_json(%{
             "tenant" => task["tenant"],
             "pool" => task["pool"],
             "capability_ref" => task_capability_ref(task),
             "payload" => task["payload"]
           })
         )
         |> Base.encode16(case: :lower))
  end

  defp duplicate_invoke?(nil, _events), do: false

  defp duplicate_invoke?(dedupe_key, events) do
    events
    |> Enum.flat_map(&canonical_effect/1)
    |> Enum.any?(fn
      %{
        "type" => "invoke_requested",
        "payload" => %{"dedupe_key" => ^dedupe_key, "admission" => %{"status" => "accepted"}}
      } ->
        true

      _effect ->
        false
    end)
  end

  defp invoke_allowed?(task, opts) do
    case allowed_capabilities(opts) do
      nil ->
        true

      [] ->
        true

      allowed ->
        case task_capability_ref(task) do
          nil -> false
          capability_ref -> capability_ref in allowed
        end
    end
  end

  defp allowed_capabilities(opts) do
    opts
    |> Keyword.get(:allowed_capabilities)
    |> Kernel.||(Keyword.get(opts, :allowlist))
    |> Kernel.||(Keyword.get(opts, :policy_allowlist))
    |> case do
      nil -> nil
      list when is_list(list) -> Enum.map(list, &to_string/1)
      value -> [to_string(value)]
    end
  end

  defp task_capability_ref(task) do
    get_in(task, ["payload", "capability_ref"]) ||
      get_in(task, ["payload", "capability_id"]) ||
      get_in(task, ["payload", "workload_id"]) ||
      pipeline_workload_ref(task["pipeline"])
  end

  defp pipeline_workload_ref("workload:" <> workload_id), do: workload_id
  defp pipeline_workload_ref(value) when is_binary(value) and value != "", do: value
  defp pipeline_workload_ref(_value), do: nil

  defp tenant_key(tenant) do
    tenant = map_or(tenant, @default_tenant)

    key =
      [
        tenant["tenant_id"],
        tenant["org"],
        tenant["org_id"],
        tenant["project"],
        tenant["project_id"]
      ]
      |> Enum.reject(&is_nil/1)
      |> Enum.join("/")

    if key == "", do: "default", else: key
  end

  defp same_tenant?(left, right), do: tenant_key(left["tenant"]) == tenant_key(right["tenant"])

  defp initial_state do
    %{"tasks" => %{}, "pools" => %{}, "invocations" => %{}}
  end

  defp apply_effect(%{"type" => "task_enqueued"} = effect, state) do
    task =
      effect
      |> payload()
      |> Map.get("task", %{})
      |> Map.merge(enqueue_ref_fields(ref(effect)))
      |> normalize_task_from_effect(effect)
      |> Map.put("status", "queued")

    put_task(state, task)
  end

  defp apply_effect(%{"type" => "invoke_requested"} = effect, state) do
    ref = ref(effect)
    payload = payload(effect)
    parent_id = ref["parent_task"]
    child_id = ref["child_task"]
    admission = payload["admission"] || %{"status" => "accepted"}

    if admission["status"] == "accepted" do
      apply_accepted_invoke(effect, state, payload, parent_id, child_id)
    else
      put_in(
        state,
        ["invocations", invocation_key(parent_id, child_id)],
        %{
          "parent_task" => parent_id,
          "child_task" => child_id,
          "return_to" => payload["return_to"],
          "status" => admission["status"],
          "reason" => admission["reason"],
          "timeout_ms" => positive_int_or_nil(payload["timeout_ms"]),
          "requested_seq" => effect["seq"]
        }
      )
    end
  end

  defp apply_effect(%{"type" => "subtask_returned"} = effect, state) do
    ref = ref(effect)
    payload = payload(effect)
    parent_id = ref["parent_task"]
    child_id = ref["child_task"]
    key = invocation_key(parent_id, child_id)

    case get_in(state, ["invocations", key, "status"]) do
      "timed_out" ->
        state

      _not_timed_out ->
        state
        |> update_in(["invocations", key], fn invocation ->
          invocation =
            invocation || %{"parent_task" => parent_id, "child_task" => child_id}

          invocation
          |> Map.put("status", "returned")
          |> Map.put("returned_seq", effect["seq"])
          |> Map.put("result", payload["result"])
        end)
        |> update_task(parent_id, fn parent ->
          if parent["status"] == "parked" and parent["invoke_waiting_for"] == child_id do
            parent
            |> Map.put("status", "queued")
            |> Map.delete("parked_reason")
            |> Map.delete("invoke_waiting_for")
            |> Map.put("last_subtask_result", payload["result"])
          else
            parent
          end
        end)
    end
  end

  defp apply_effect(%{"type" => "subtask_timed_out"} = effect, state) do
    ref = ref(effect)
    payload = payload(effect)
    parent_id = ref["parent_task"]
    child_id = ref["child_task"]
    key = invocation_key(parent_id, child_id)
    result = payload["result"] || timeout_result(payload["timeout_ms"])

    case get_in(state, ["invocations", key, "status"]) do
      "returned" ->
        state

      _not_returned ->
        state
        |> update_in(["invocations", key], fn invocation ->
          invocation =
            invocation || %{"parent_task" => parent_id, "child_task" => child_id}

          invocation
          |> Map.put("status", "timed_out")
          |> Map.put("timed_out_seq", effect["seq"])
          |> Map.put("timeout_ms", positive_int_or_nil(payload["timeout_ms"]))
          |> Map.put("result", result)
        end)
        |> update_task(child_id, fn child ->
          if child["status"] in ["done", "failed", "cancelled"] do
            child
          else
            child
            |> Map.put("status", "failed")
            |> Map.put("status_reason", "timeout")
            |> Map.put("result", result)
          end
        end)
        |> update_task(parent_id, fn parent ->
          if parent["status"] == "parked" and parent["invoke_waiting_for"] == child_id do
            parent
            |> Map.put("status", "queued")
            |> Map.delete("parked_reason")
            |> Map.delete("invoke_waiting_for")
            |> Map.put("last_subtask_result", result)
          else
            parent
          end
        end)
    end
  end

  defp apply_effect(%{"type" => type} = effect, state)
       when type in [
              "task_blocked",
              "task_scheduled",
              "task_running",
              "task_done",
              "task_failed",
              "task_parked",
              "task_cancelled"
            ] do
    status = effect_status(type)
    task_id = ref(effect)["task_id"]
    payload = payload(effect)

    update_task(state, task_id, fn task ->
      task
      |> Map.put("status", status)
      |> Map.update("runs", run_list(payload["run_id"]), &merge_runs(&1, payload["run_id"]))
      |> put_optional("status_reason", payload["reason"])
      |> put_optional("result", payload["result"])
    end)
  end

  defp apply_effect(_effect, state), do: state

  defp apply_accepted_invoke(effect, state, payload, parent_id, child_id) do
    child =
      payload
      |> Map.get("task", %{})
      |> Map.put_new("id", child_id)
      |> normalize_task_from_effect(effect)
      |> Map.put("status", "queued")

    invocation = %{
      "parent_task" => parent_id,
      "child_task" => child["id"],
      "return_to" => payload["return_to"],
      "status" => "requested",
      "timeout_ms" => positive_int_or_nil(payload["timeout_ms"]),
      "requested_at" => effect["ts"],
      "requested_seq" => effect["seq"]
    }

    state
    |> put_task(child)
    |> update_task(parent_id, fn parent ->
      parent
      |> Map.put("status", "parked")
      |> Map.put("parked_reason", "awaiting-invoke")
      |> Map.put("invoke_waiting_for", child["id"])
    end)
    |> put_in(["invocations", invocation_key(parent_id, child["id"])], invocation)
  end

  defp normalize_task_from_effect(attrs, effect) do
    attrs
    |> task()
    |> put_in(["ts", "created"], get_in(task(attrs), ["ts", "created"]) || effect["ts"])
    |> put_in(["ts", "updated"], effect["ts"])
    |> Map.put("_enqueued_seq", effect["seq"])
  end

  defp put_task(state, task) do
    state
    |> put_in(["tasks", task["id"]], task)
    |> put_in(["pools", task["pool"]], pool_projection(state, task["pool"]))
  end

  defp update_task(state, nil, _fun), do: state

  defp update_task(state, task_id, fun) do
    update_in(state, ["tasks", task_id], fn
      nil -> fun.(task(%{"id" => task_id}))
      task -> fun.(task)
    end)
  end

  defp pool_projection(state, pool_id) do
    get_in(state, ["pools", pool_id]) || %{"id" => pool_id, "discipline" => "fifo"}
  end

  defp materialize_data_dependencies(state) do
    tasks = Map.get(state, "tasks", %{})

    updated_tasks =
      Map.new(tasks, fn {task_id, task} ->
        {task_id, materialize_task_data_dependencies(task, state)}
      end)

    Map.put(state, "tasks", updated_tasks)
  end

  defp materialize_task_data_dependencies(task, state) do
    task
    |> Map.get("deps", [])
    |> Enum.filter(&is_map/1)
    |> Enum.filter(&(dep_payload(&1) == "data"))
    |> Enum.filter(&dep_met?(&1, state))
    |> Enum.reduce(task, fn dep, task ->
      from = dep["from"] || dep["task_id"]
      result = get_in(state, ["tasks", from, "result"])

      if is_nil(result) do
        task
      else
        key = dep["as"] || from

        task
        |> update_in(["payload"], fn payload -> map_or(payload, %{}) end)
        |> update_in(["payload", "dependency_results"], fn results -> map_or(results, %{}) end)
        |> put_in(["payload", "dependency_results", key], result)
      end
    end)
  end

  defp finalize_ready_sets(state) do
    cycles =
      case dependency_cycle(state) do
        nil -> []
        cycle -> [cycle]
      end

    state
    |> Map.put("ready_tasks", Enum.map(ready_tasks(state), & &1["id"]))
    |> Map.put("blocked_tasks", Enum.map(blocked_tasks(state), & &1["id"]))
    |> Map.put("cycles", cycles)
  end

  defp ready_task?(%{"status" => "queued"} = task, state), do: hard_deps_met?(task, state)
  defp ready_task?(_task, _state), do: false

  defp blocked_tasks(state) do
    state
    |> Map.get("tasks", %{})
    |> Map.values()
    |> Enum.filter(fn task ->
      task["status"] in ["queued", "blocked"] and not hard_deps_met?(task, state)
    end)
  end

  defp hard_deps_met?(task, state) do
    deps =
      task
      |> Map.get("deps", [])
      |> Enum.filter(&is_map/1)
      |> Enum.filter(&enabling_dep?/1)

    case join_or(task["join"], %{"rule" => "all"}) do
      %{"rule" => "any"} ->
        deps == [] or Enum.any?(deps, &dep_met?(&1, state))

      %{"rule" => "k_of_n", "k" => k} when is_integer(k) and k > 0 ->
        Enum.count(deps, &dep_met?(&1, state)) >= k

      _all ->
        Enum.all?(deps, &dep_met?(&1, state))
    end
  end

  defp dep_met?(dep, state) do
    from = dep["from"] || dep["task_id"]
    relation = dep["relation"] || "FS"
    upstream = get_in(state, ["tasks", from])

    case {relation, upstream && upstream["status"]} do
      {relation, "done"} when relation in ["FS", "FF", "finish→start", "finish->start"] ->
        dep_guard_matches?(dep, upstream)

      {relation, "running"} when relation in ["SS", "SF", "start→start", "start->start"] ->
        dep_guard_matches?(dep, upstream)

      {_relation, _status} ->
        false
    end
  end

  defp dep_strength(%{} = dep), do: dep["strength"] || "hard"
  defp dep_payload(%{} = dep), do: dep["payload"] || "control"

  defp dep_guard_matches?(dep, upstream) do
    case dep_guard(dep) do
      nil -> true
      "unconditional" -> true
      guard -> guard_match?(guard, Map.get(upstream || %{}, "result", %{}))
    end
  end

  defp dep_guard(%{} = dep), do: dep["guard"] || dep["predicate"] || dep["if"]

  defp guard_match?(%{} = guard, result) do
    guard = map_or(guard, %{})
    result = map_or(result, %{})

    cond do
      is_map(guard["result"]) ->
        map_subset?(guard["result"], result)

      is_map(guard["equals"]) ->
        map_subset?(guard["equals"], result)

      true ->
        map_subset?(guard, result)
    end
  end

  defp guard_match?(guard, result) when is_binary(guard) do
    result = map_or(result, %{})
    guard in [result["signal"], result["status"], result["result"]]
  end

  defp guard_match?(guard, result) when is_boolean(guard) do
    result = map_or(result, %{})
    guard == result["ok?"] or guard == result["ok"]
  end

  defp guard_match?(_guard, _result), do: false

  defp map_subset?(expected, actual) do
    Enum.all?(expected, fn {key, value} ->
      actual_value = actual[key] || actual[to_string(key)]

      cond do
        is_map(value) and is_map(actual_value) -> map_subset?(value, actual_value)
        true -> actual_value == value
      end
    end)
  end

  defp filter_pool(tasks, nil), do: tasks
  defp filter_pool(tasks, pool_id), do: Enum.filter(tasks, &(&1["pool"] == pool_id))

  defp dependency_cycle(state) do
    graph = dependency_graph(state)

    graph
    |> Map.keys()
    |> Enum.reduce_while(MapSet.new(), fn node, visited ->
      case visit_dependency_node(node, graph, visited, [], MapSet.new()) do
        {:ok, visited} -> {:cont, visited}
        {:cycle, cycle} -> {:halt, cycle}
      end
    end)
    |> case do
      %MapSet{} -> nil
      cycle -> cycle
    end
  end

  defp dependency_graph(state) do
    state
    |> Map.get("tasks", %{})
    |> Enum.reduce(%{}, fn {task_id, task}, graph ->
      graph =
        graph
        |> Map.put_new(task_id, [])

      task
      |> Map.get("deps", [])
      |> Enum.filter(&enabling_dep?/1)
      |> Enum.reduce(graph, fn dep, graph ->
        case dep["from"] || dep["task_id"] do
          from when is_binary(from) and from != "" ->
            graph
            |> Map.put_new(from, [])
            |> Map.update!(from, &Enum.uniq([task_id | &1]))

          _ ->
            graph
        end
      end)
    end)
  end

  defp visit_dependency_node(node, graph, visited, path, stack) do
    cond do
      MapSet.member?(stack, node) ->
        {:cycle, close_cycle(path, node)}

      MapSet.member?(visited, node) ->
        {:ok, visited}

      true ->
        visited = MapSet.put(visited, node)
        stack = MapSet.put(stack, node)
        path = [node | path]

        graph
        |> Map.get(node, [])
        |> Enum.reduce_while({:ok, visited}, fn next, {:ok, visited} ->
          case visit_dependency_node(next, graph, visited, path, stack) do
            {:ok, visited} -> {:cont, {:ok, visited}}
            {:cycle, cycle} -> {:halt, {:cycle, cycle}}
          end
        end)
    end
  end

  defp close_cycle(path, node) do
    path
    |> Enum.reverse()
    |> Enum.drop_while(&(&1 != node))
    |> Kernel.++([node])
  end

  defp enabling_dep?(%{} = dep) do
    relation = dep["relation"] || "FS"
    constraint = dep["constraint"]

    dep_strength(dep) == "hard" and
      relation not in ["conflict", "mutex", "affinity"] and
      constraint not in ["conflict", "mutex", "affinity"]
  end

  defp enabling_dep?(_dep), do: false

  defp reject_conflicts(tasks, state) do
    running =
      state
      |> Map.get("tasks", %{})
      |> Map.values()
      |> Enum.filter(&(&1["status"] == "running"))
      |> Map.new(&{&1["id"], &1})

    Enum.reject(tasks, fn task ->
      Enum.any?(running, fn {_id, running_task} ->
        same_tenant?(task, running_task) and conflicts?(task, running_task)
      end)
    end)
  end

  defp conflicts?(left, right) do
    declares_conflict?(left, right["id"]) or declares_conflict?(right, left["id"])
  end

  defp declares_conflict?(task, other_id) when is_binary(other_id) do
    task
    |> Map.get("deps", [])
    |> Enum.filter(&is_map/1)
    |> Enum.any?(fn dep ->
      dep["from"] == other_id and
        (dep["relation"] in ["conflict", "mutex"] or dep["constraint"] in ["conflict", "mutex"])
    end)
  end

  defp declares_conflict?(_task, _other_id), do: false

  defp reject_tenant_capacity(tasks, _state, %{"concurrency" => nil}), do: tasks

  defp reject_tenant_capacity(tasks, state, %{"concurrency" => limit, "id" => pool_id})
       when is_integer(limit) and limit > 0 do
    active_counts =
      state
      |> Map.get("tasks", %{})
      |> Map.values()
      |> Enum.filter(&(&1["pool"] == pool_id and &1["status"] in ["scheduled", "running"]))
      |> Enum.frequencies_by(&tenant_key(&1["tenant"]))

    Enum.reject(tasks, fn task ->
      Map.get(active_counts, tenant_key(task["tenant"]), 0) >= limit
    end)
  end

  defp reject_tenant_capacity(tasks, _state, _pool), do: tasks

  defp pool_config(pool_id) when is_binary(pool_id),
    do: %{"id" => pool_id, "discipline" => "fifo"}

  defp pool_config(%{} = pool) do
    pool = stringify(pool)

    %{
      "id" => string_or(pool["id"], "default"),
      "scope" => string_or(pool["scope"], nil),
      "tenant" => map_or_nil(pool["tenant"]),
      "discipline" => normalize_discipline(pool["discipline"]),
      "concurrency" => positive_int_or_nil(pool["concurrency"]),
      "fairness_key" => string_or(pool["fairness_key"], "tenant")
    }
  end

  defp filter_scope(tasks, %{"scope" => scope, "tenant" => %{} = tenant})
       when scope in ["org", "project", "run"] do
    Enum.filter(tasks, &tenant_scope_match?(&1["tenant"], tenant, scope))
  end

  defp filter_scope(tasks, _pool), do: tasks

  defp tenant_scope_match?(task_tenant, pool_tenant, "org") do
    tenant_org(task_tenant) == tenant_org(pool_tenant)
  end

  defp tenant_scope_match?(task_tenant, pool_tenant, "project") do
    tenant_org(task_tenant) == tenant_org(pool_tenant) and
      tenant_project(task_tenant) == tenant_project(pool_tenant)
  end

  defp tenant_scope_match?(task_tenant, pool_tenant, "run") do
    tenant_scope_match?(task_tenant, pool_tenant, "project") and
      tenant_run(task_tenant) == tenant_run(pool_tenant)
  end

  defp tenant_org(tenant) do
    tenant = map_or(tenant, %{})
    tenant["org"] || tenant["org_id"] || tenant["tenant_id"]
  end

  defp tenant_project(tenant) do
    tenant = map_or(tenant, %{})
    tenant["project"] || tenant["project_id"]
  end

  defp tenant_run(tenant) do
    tenant = map_or(tenant, %{})
    tenant["run"] || tenant["run_id"]
  end

  defp normalize_discipline(nil), do: "fifo"

  defp normalize_discipline(value) when is_atom(value),
    do: normalize_discipline(Atom.to_string(value))

  defp normalize_discipline("deadline_edf"), do: "deadline_edf"
  defp normalize_discipline("deadline"), do: "deadline_edf"

  defp normalize_discipline(value)
       when value in ["fifo", "lifo", "priority", "weighted_fair", "random", "agent_pick"],
       do: value

  defp normalize_discipline(_value), do: "fifo"

  defp agent_pick(candidates, opts) do
    case Keyword.get(opts, :selector) do
      selector when is_function(selector, 1) -> {:ok, selector.(candidates)}
      _selector -> {:error, :agent_pick_requires_selector}
    end
  end

  defp pick("fifo", candidates, _state, _pool),
    do: Enum.min_by(candidates, &sort_key(&1, :oldest))

  defp pick("lifo", candidates, _state, _pool),
    do: Enum.max_by(candidates, &sort_key(&1, :oldest))

  defp pick("priority", candidates, _state, _pool) do
    Enum.min_by(candidates, fn task -> {-task["priority"], sort_key(task, :oldest)} end)
  end

  defp pick("deadline_edf", candidates, _state, _pool) do
    Enum.min_by(candidates, fn task -> {deadline(task), sort_key(task, :oldest)} end)
  end

  defp pick("weighted_fair", candidates, state, pool) do
    fairness_key = pool["fairness_key"] || "tenant"
    running_counts = running_counts_by_fairness_key(state, pool["id"], fairness_key)

    candidates
    |> Enum.group_by(&fairness_value(&1, fairness_key))
    |> Enum.map(fn {value, tasks} ->
      {Map.get(running_counts, value, 0), value, Enum.min_by(tasks, &sort_key(&1, :oldest))}
    end)
    |> Enum.min_by(fn {running_count, value, task} ->
      {running_count, value, sort_key(task, :oldest)}
    end)
    |> elem(2)
  end

  defp pick("random", candidates, _state, _pool),
    do: Enum.min_by(candidates, &stable_hash(&1["id"]))

  defp pick(_discipline, candidates, state, pool), do: pick("fifo", candidates, state, pool)

  defp running_counts_by_fairness_key(state, pool_id, fairness_key) do
    state
    |> Map.get("tasks", %{})
    |> Map.values()
    |> Enum.filter(&(&1["pool"] == pool_id and &1["status"] == "running"))
    |> Enum.frequencies_by(&fairness_value(&1, fairness_key))
  end

  defp fairness_value(task, "tenant") do
    tenant = map_or(task["tenant"], %{})

    value =
      [
        tenant["tenant_id"],
        tenant["org"],
        tenant["org_id"],
        tenant["project"],
        tenant["project_id"]
      ]
      |> Enum.reject(&is_nil/1)
      |> Enum.join("/")

    if value == "", do: "default", else: value
  end

  defp fairness_value(task, "user") do
    get_in(task, ["tenant", "user_id"]) ||
      get_in(task, ["tenant", "actor_id"]) ||
      get_in(task, ["provenance", "user_id"]) ||
      "default"
  end

  defp fairness_value(task, key), do: to_string(get_in(task, ["payload", key]) || "default")

  defp sort_key(task, :oldest),
    do: task["_enqueued_seq"] || get_in(task, ["ts", "created"]) || task["id"]

  defp deadline(task) do
    get_in(task, ["payload", "deadline_at"]) ||
      get_in(task, ["ts", "deadline_at"]) ||
      "9999-12-31T23:59:59Z"
  end

  defp stable_hash(value) do
    :crypto.hash(:sha256, to_string(value))
    |> binary_part(0, 8)
  end

  defp canonical_effect(%{"kind" => "effect"} = event), do: [event]
  defp canonical_effect(%{"event" => %{"kind" => "effect"} = event}), do: [event]
  defp canonical_effect(_event), do: []

  defp enqueue_ref_fields(ref) do
    ref
    |> Map.take(["pool"])
    |> put_optional("id", ref["task_id"])
  end

  defp status_effect("blocked"), do: "task_blocked"
  defp status_effect("scheduled"), do: "task_scheduled"
  defp status_effect("running"), do: "task_running"
  defp status_effect("done"), do: "task_done"
  defp status_effect("failed"), do: "task_failed"
  defp status_effect("parked"), do: "task_parked"
  defp status_effect("cancelled"), do: "task_cancelled"

  defp effect_status("task_blocked"), do: "blocked"
  defp effect_status("task_scheduled"), do: "scheduled"
  defp effect_status("task_running"), do: "running"
  defp effect_status("task_done"), do: "done"
  defp effect_status("task_failed"), do: "failed"
  defp effect_status("task_parked"), do: "parked"
  defp effect_status("task_cancelled"), do: "cancelled"

  defp payload(event), do: map_or(event["payload"], %{})
  defp ref(event), do: map_or(event["ref"], %{})

  defp invocation_key(parent_id, child_id), do: "#{parent_id}->#{child_id}"

  defp run_list(nil), do: []
  defp run_list(run_id), do: [run_id]

  defp merge_runs(runs, nil), do: list_or(runs)
  defp merge_runs(runs, run_id), do: Enum.uniq(list_or(runs) ++ [run_id])

  defp canonical_json(%{} = map) do
    body =
      map
      |> Enum.sort_by(fn {key, _value} -> to_string(key) end)
      |> Enum.map_join(",", fn {key, value} ->
        Jason.encode!(to_string(key)) <> ":" <> canonical_json(value)
      end)

    "{" <> body <> "}"
  end

  defp canonical_json(list) when is_list(list),
    do: "[" <> Enum.map_join(list, ",", &canonical_json/1) <> "]"

  defp canonical_json(value), do: Jason.encode!(value)

  defp required_string!(map, key) do
    value = map[key]

    if is_binary(value) and value != "" do
      value
    else
      raise ArgumentError, "#{key} must be a non-empty string"
    end
  end

  defp string_or(value, _default) when is_binary(value) and value != "", do: value
  defp string_or(_value, default), do: default

  defp int_or(value, _default) when is_integer(value), do: value

  defp int_or(value, default) when is_binary(value) do
    case Integer.parse(value) do
      {int, ""} -> int
      _ -> default
    end
  end

  defp int_or(_value, default), do: default

  defp numeric(value) when is_integer(value), do: value * 1.0
  defp numeric(value) when is_float(value), do: value

  defp numeric(value) when is_binary(value) do
    case Float.parse(value) do
      {float, ""} -> float
      _ -> 0.0
    end
  end

  defp numeric(_value), do: 0.0

  defp positive_int_or_nil(value) do
    case int_or(value, nil) do
      int when is_integer(int) and int > 0 -> int
      _value -> nil
    end
  end

  defp invoke_expired?(%{"status" => "requested"} = invocation, now_ms)
       when is_integer(now_ms) do
    timeout_ms = positive_int_or_nil(invocation["timeout_ms"])
    requested_ms = timestamp_ms(invocation["requested_at"])

    is_integer(timeout_ms) and is_integer(requested_ms) and
      requested_ms + timeout_ms <= now_ms
  end

  defp invoke_expired?(_invocation, _now_ms), do: false

  defp timestamp_ms(value) when is_integer(value), do: value

  defp timestamp_ms(%DateTime{} = value), do: DateTime.to_unix(value, :millisecond)

  defp timestamp_ms(value) when is_binary(value) do
    case DateTime.from_iso8601(value) do
      {:ok, value, _offset} -> DateTime.to_unix(value, :millisecond)
      _error -> nil
    end
  end

  defp timestamp_ms(_value), do: nil

  defp timeout_result(timeout_ms) do
    %{"ok" => false, "error" => "timeout"}
    |> put_optional("timeout_ms", timeout_ms)
  end

  defp join_or("all", _default), do: %{"rule" => "all"}
  defp join_or(:all, default), do: join_or("all", default)
  defp join_or("any", _default), do: %{"rule" => "any"}
  defp join_or(:any, default), do: join_or("any", default)

  defp join_or("k_of_n", _default), do: %{"rule" => "k_of_n", "k" => 1}
  defp join_or(:k_of_n, default), do: join_or("k_of_n", default)

  defp join_or(%{} = join, default) do
    join = stringify(join)
    rule = join["rule"] || join["type"] || join["mode"]

    case rule do
      "all" ->
        %{"rule" => "all"}

      "any" ->
        %{"rule" => "any"}

      "k_of_n" ->
        case positive_int_or_nil(join["k"] || join["n"] || join["quorum"]) do
          k when is_integer(k) -> %{"rule" => "k_of_n", "k" => k}
          nil -> default
        end

      _other ->
        default
    end
  end

  defp join_or(_join, default), do: default

  defp status_or(value, _default) when value in @task_statuses, do: value

  defp status_or(value, default) when is_atom(value),
    do: status_or(Atom.to_string(value), default)

  defp status_or(_value, default), do: default

  defp list_or(value) when is_list(value), do: Enum.map(value, &stringify/1)
  defp list_or(_value), do: []

  defp map_or(%{} = map, _default), do: stringify(map)
  defp map_or(_value, default), do: default

  defp map_or_nil(%{} = map), do: stringify(map)
  defp map_or_nil(_value), do: nil

  defp put_optional(map, _key, nil), do: map
  defp put_optional(map, key, value), do: Map.put(map, key, value)

  defp stringify(%{} = map),
    do: Map.new(map, fn {key, value} -> {to_string(key), stringify(value)} end)

  defp stringify(list) when is_list(list), do: Enum.map(list, &stringify/1)
  defp stringify(value), do: value
end
