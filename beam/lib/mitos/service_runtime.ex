defmodule Mitos.ServiceRuntime do
  @moduledoc """
  Service-unit intake for the work-pool plane.

  A service owns triggers and lifecycle policy. When a trigger fires it produces
  a durable Task in a pool; the drainer later dispatches that Task. This module
  keeps the first service-runtime slice deliberately narrow: normalize units,
  enforce the platform-vs-tenant execution boundary, and append trigger/enqueue
  Effects to the canonical log.
  """

  alias Mitos.Trace

  @execution_types ~w(pipeline capability internal)
  @tenant_execution_types ~w(pipeline capability)
  @scopes ~w(platform org project run)
  @trigger_types ~w(manual webhook event external_signal interval cron continuous)
  @if_running_values ~w(queue skip kill)
  @health_types ~w(file command heartbeat)
  @health_statuses ~w(healthy unhealthy unknown)
  @condition_types ~w(file_exists env_present env_equals command)
  @on_unhealthy_values ~w(log kill restart)
  @restart_actions ~w(retry skip abort)
  @running_statuses ~w(scheduled running parked)
  @default_tenant %{"org" => "default", "project" => "default"}

  @doc "Normalize and validate a service unit."
  def unit(attrs, opts \\ []) when is_map(attrs) and is_list(opts) do
    attrs = stringify(attrs)
    execution = map_or(attrs["execution"], %{})
    execution_type = string_or(execution["type"], nil)
    platform? = Keyword.get(opts, :platform?, false)
    scope = string_or(attrs["scope"], if(platform?, do: "platform", else: "project"))

    with :ok <- valid_scope(scope),
         :ok <- valid_execution_type(execution_type),
         :ok <- valid_tenancy_boundary(execution_type, scope, platform?) do
      %{
        "id" => required_string!(attrs, "id"),
        "scope" => scope,
        "tenant" => map_or(attrs["tenant"], @default_tenant),
        "schedule" => map_or(attrs["schedule"], %{"type" => "manual"}),
        "execution" => normalize_execution(execution),
        "pool" => string_or(attrs["pool"], nil),
        "priority" =>
          int_or(attrs["priority"], int_or(get_in(attrs, ["concurrency", "priority"]), 0)),
        "concurrency" => normalize_concurrency(map_or(attrs["concurrency"], %{})),
        "health" => normalize_health(attrs["health"] || attrs["health_check"]),
        "circuit_breaker" => normalize_circuit_breaker(attrs["circuit_breaker"]),
        "conditions" => normalize_conditions(attrs["conditions"] || attrs["condition"]),
        "restart_policy" => normalize_restart_policy(attrs["restart_policy"] || attrs["restart"]),
        "triggers" => map_or(attrs["triggers"], %{}),
        "depends_on" => list_or(attrs["depends_on"]),
        "part_of" => list_or(attrs["part_of"]),
        "wants" => list_or(attrs["wants"]),
        "requires" => list_or(attrs["requires"]),
        "conflicts_with" => list_or(attrs["conflicts_with"]),
        "before" => list_or(attrs["before"]),
        "after" => list_or(attrs["after"])
      }
    end
  end

  @doc """
  Append a service trigger Effect and enqueue the Task it produced.

  The returned Task is canonicalized by `Mitos.WorkPool.task/1`.
  """
  def append_trigger(%Trace{} = trace, unit_attrs, trigger_attrs, opts)
      when is_map(unit_attrs) and is_map(trigger_attrs) and is_list(opts) do
    caused_by = Keyword.fetch!(opts, :caused_by)
    unit = unit!(unit_attrs, opts)
    trigger_attrs = maybe_put_now(trigger_attrs, Keyword.get(opts, :now_ms))
    trigger = trigger(trigger_attrs)
    task = task_from_trigger(unit, trigger, opts)
    now_ms = Keyword.get_lazy(opts, :now_ms, fn -> System.system_time(:millisecond) end)
    events = Keyword.get_lazy(opts, :events, fn -> read_trace_events(trace) end)
    service_state = fold_events(events)
    pool_state = Mitos.WorkPool.fold_events(events)

    case admit_trigger(
           trace,
           unit,
           trigger,
           task,
           service_state,
           pool_state,
           now_ms,
           caused_by,
           opts
         ) do
      {:enqueue, trace} ->
        trace =
          Trace.append_effect(trace, %{
            type: "service_triggered",
            caused_by: caused_by,
            ref: %{
              "service_id" => unit["id"],
              "trigger_type" => trigger["type"],
              "task_id" => task["id"]
            },
            payload: %{
              "unit" => public_unit(unit),
              "trigger" => trigger,
              "task" => task
            }
          })

        {Mitos.WorkPool.append_enqueue(trace, task, caused_by: caused_by), task}

      {:skip, trace, reason, snapshot} ->
        {append_trigger_skipped(trace, unit, trigger, task, reason, snapshot, caused_by), nil}
    end
  end

  @doc "Append a health-check observation for a service unit."
  def append_health_result(%Trace{} = trace, unit_attrs, check_attrs, opts)
      when is_map(unit_attrs) and is_map(check_attrs) and is_list(opts) do
    caused_by = Keyword.fetch!(opts, :caused_by)
    now_ms = Keyword.get_lazy(opts, :now_ms, fn -> System.system_time(:millisecond) end)
    unit = unit!(unit_attrs, opts)
    check = health_result(unit, check_attrs, now_ms)
    events = Keyword.get_lazy(opts, :events, fn -> read_trace_events(trace) end)
    service_state = fold_events(events)
    pool_state = Mitos.WorkPool.fold_events(events)

    trace =
      Trace.append_effect(trace, %{
        type: "service_health_checked",
        caused_by: caused_by,
        ref: %{"service_id" => unit["id"], "health_type" => check["type"]},
        payload: %{
          "unit" => public_unit(unit),
          "check" => check
        }
      })

    trace =
      case check["status"] do
        "unhealthy" ->
          trace
          |> append_unhealthy_action(unit, check, service_state, pool_state, caused_by)
          |> maybe_open_circuit(unit, service_state, check, caused_by)

        "healthy" ->
          maybe_close_circuit(trace, unit, service_state, check, caused_by)

        _status ->
          trace
      end

    {trace, check}
  end

  @doc """
  Fire due tick-driven service units and append the resulting Tasks.

  Handles `interval` and `continuous` schedules. Webhook/manual/external signal
  triggers are explicit ingress and should call `append_trigger/4` directly.
  """
  def append_due_triggers(%Trace{} = trace, active_set_or_units, opts)
      when is_list(opts) do
    caused_by = Keyword.fetch!(opts, :caused_by)
    now_ms = Keyword.get_lazy(opts, :now_ms, fn -> System.system_time(:millisecond) end)
    events = Keyword.get_lazy(opts, :events, fn -> read_trace_events(trace) end)
    service_state = fold_events(events)
    pool_state = Mitos.WorkPool.fold_events(events)

    active_set_or_units
    |> active_units()
    |> Enum.reduce({trace, []}, fn unit, {trace, tasks} ->
      case due_trigger(unit, service_state, pool_state, now_ms) do
        nil ->
          {trace, tasks}

        trigger_attrs ->
          trigger_opts =
            opts
            |> Keyword.put(:caused_by, caused_by)
            |> Keyword.put(:now_ms, now_ms)
            |> Keyword.put(:platform?, unit["scope"] == "platform")

          case append_trigger(trace, unit, trigger_attrs, trigger_opts) do
            {trace, nil} -> {trace, tasks}
            {trace, task} -> {trace, [task | tasks]}
          end
      end
    end)
    |> then(fn {trace, tasks} -> {trace, Enum.reverse(tasks)} end)
  end

  @doc "Fire service units whose event trigger matches a canonical Effect/Event."
  def append_event_triggers(%Trace{} = trace, active_set_or_units, event, opts)
      when is_map(event) and is_list(opts) do
    caused_by = Keyword.fetch!(opts, :caused_by)
    now_ms = Keyword.get_lazy(opts, :now_ms, fn -> System.system_time(:millisecond) end)
    event = stringify(event)

    active_set_or_units
    |> active_units()
    |> Enum.reduce({trace, []}, fn unit, {trace, tasks} ->
      if event_trigger_matches?(unit, event) do
        trigger_opts =
          opts
          |> Keyword.put(:caused_by, caused_by)
          |> Keyword.put(:now_ms, now_ms)
          |> Keyword.put(:platform?, unit["scope"] == "platform")

        case append_trigger(
               trace,
               unit,
               %{
                 type: "event",
                 event_type: event_type(event),
                 idempotency_key: event_idempotency_key(unit, event),
                 inputs: %{"event" => event}
               },
               trigger_opts
             ) do
          {trace, nil} -> {trace, tasks}
          {trace, task} -> {trace, [task | tasks]}
        end
      else
        {trace, tasks}
      end
    end)
    |> then(fn {trace, tasks} -> {trace, Enum.reverse(tasks)} end)
  end

  @doc "Apply service restart policies to a failed service outcome event."
  def append_restart_triggers(%Trace{} = trace, active_set_or_units, event, opts)
      when is_map(event) and is_list(opts) do
    caused_by = Keyword.fetch!(opts, :caused_by)
    now_ms = Keyword.get_lazy(opts, :now_ms, fn -> System.system_time(:millisecond) end)
    events = Keyword.get_lazy(opts, :events, fn -> read_trace_events(trace) end)
    service_state = fold_events(events)
    event = stringify(event)

    case failed_service_id(event) do
      nil ->
        {trace, []}

      service_id ->
        active_set_or_units
        |> active_units()
        |> Enum.filter(&(&1["id"] == service_id))
        |> Enum.reduce({trace, []}, fn unit, {trace, decisions} ->
          {trace, decision} =
            append_restart_decision(trace, unit, event, service_state, now_ms, caused_by)

          {trace, [decision | decisions]}
        end)
        |> then(fn {trace, decisions} ->
          {trace, Enum.reverse(Enum.reject(decisions, &is_nil/1))}
        end)
    end
  end

  @doc "Enqueue restart Tasks whose durable backoff schedule is due."
  def append_due_restarts(%Trace{} = trace, active_set_or_units, opts) when is_list(opts) do
    caused_by = Keyword.fetch!(opts, :caused_by)
    now_ms = Keyword.get_lazy(opts, :now_ms, fn -> System.system_time(:millisecond) end)
    events = Keyword.get_lazy(opts, :events, fn -> read_trace_events(trace) end)
    service_state = fold_events(events)
    units = active_units(active_set_or_units)

    units
    |> Enum.reduce({trace, []}, fn unit, {trace, tasks} ->
      due_restart_schedules(service_state, unit["id"], now_ms)
      |> Enum.reduce({trace, tasks}, fn schedule, {trace, tasks} ->
        trigger_attrs = %{
          type: "event",
          event_type: "service.restart:#{unit["id"]}",
          idempotency_key: schedule["id"],
          inputs: %{"restart" => schedule},
          received_at_ms: now_ms
        }

        trace =
          Trace.append_effect(trace, %{
            type: "service_restart_fired",
            caused_by: caused_by,
            ref: %{
              "service_id" => unit["id"],
              "restart_id" => schedule["id"],
              "failed_task_id" => schedule["failed_task_id"]
            },
            payload: %{"schedule" => schedule, "fired_at_ms" => now_ms}
          })

        trigger_opts =
          opts
          |> Keyword.put(:caused_by, caused_by)
          |> Keyword.put(:now_ms, now_ms)
          |> Keyword.put(:platform?, unit["scope"] == "platform")

        case append_trigger(trace, unit, trigger_attrs, trigger_opts) do
          {trace, nil} -> {trace, tasks}
          {trace, task} -> {trace, [task | tasks]}
        end
      end)
    end)
    |> then(fn {trace, tasks} -> {trace, Enum.reverse(tasks)} end)
  end

  @doc "Build the Task that a service trigger would enqueue."
  def task_from_trigger(unit_attrs, trigger_attrs, opts \\ [])
      when is_map(unit_attrs) and is_map(trigger_attrs) and is_list(opts) do
    unit = if Map.has_key?(unit_attrs, "execution"), do: unit_attrs, else: unit!(unit_attrs, opts)

    trigger =
      if Map.has_key?(trigger_attrs, "type"), do: trigger_attrs, else: trigger(trigger_attrs)

    task_id = string_or(Keyword.get(opts, :task_id), default_task_id(unit, trigger))
    execution = unit["execution"]

    Mitos.WorkPool.task(%{
      "id" => task_id,
      "tenant" => Map.get(unit, "tenant", @default_tenant),
      "source" => "service:#{unit["id"]}:#{trigger["type"]}",
      "pool" => string_or(Keyword.get(opts, :pool), pool_for(unit, execution)),
      "priority" => int_or(Keyword.get(opts, :priority), unit["priority"]),
      "pipeline" => execution["pipeline"],
      "payload" => %{
        "service_id" => unit["id"],
        "trigger" => trigger,
        "execution" => execution,
        "inputs" => map_or(trigger["inputs"], %{})
      },
      "budget" => map_or(Keyword.get(opts, :budget), %{}),
      "provenance" => %{
        "created_by" => "service_runtime",
        "service_id" => unit["id"],
        "trigger_type" => trigger["type"]
      },
      "external" => external_ref(trigger)
    })
  end

  @doc """
  Resolve active service units from targets, presets, pipelines, and overrides.

  The returned view explains why each unit is active or masked. Hard-vs-hard
  conflicts return a load-time error instead of being resolved silently.
  """
  def resolve_active_set(config) when is_map(config) do
    config = stringify(config)
    units = index_by_id(config["units"])
    targets = index_by_id(config["targets"])
    presets = list_or(config["presets"])
    pipelines = index_by_id(config["pipelines"])
    project_type = config["project_type"]

    resolution =
      empty_resolution()
      |> apply_presets(presets, project_type, units)
      |> apply_targets(list_or(config["active_targets"]), targets, units)
      |> apply_pipelines(list_or(config["active_pipelines"]), pipelines, targets, units)
      |> apply_explicit_enables(list_or(config["explicit_enable"] || config["enable"]), units)
      |> apply_explicit_disables(list_or(config["explicit_disable"] || config["disable"]))
      |> expand_unit_relations(units)

    case resolve_unit_conflicts(resolution, units) do
      {:ok, resolution} -> {:ok, active_set_view(resolution, units)}
      {:error, reason} -> {:error, reason}
    end
  end

  @doc "Fold service-runtime Effects into per-service trigger state."
  def fold_events(events) when is_list(events) do
    events
    |> Enum.flat_map(&canonical_effect/1)
    |> Enum.sort_by(&Map.get(&1, "seq", -1))
    |> Enum.reduce(initial_service_state(), &apply_service_effect/2)
    |> Map.delete("_task_index")
  end

  defp unit!(attrs, opts) do
    case unit(attrs, opts) do
      {:error, reason} -> raise ArgumentError, inspect(reason)
      unit -> unit
    end
  end

  defp trigger(attrs) do
    attrs = stringify(attrs)
    type = string_or(attrs["type"], "manual")

    unless type in @trigger_types do
      raise ArgumentError, "trigger.type must be one of #{Enum.join(@trigger_types, ", ")}"
    end

    %{
      "type" => type,
      "idempotency_key" => string_or(attrs["idempotency_key"], nil),
      "delivery_id" => string_or(attrs["delivery_id"], nil),
      "event_type" => string_or(attrs["event_type"], nil),
      "external_ref" => attrs["external_ref"],
      "inputs" => map_or(attrs["inputs"], %{}),
      "received_at" => attrs["received_at"],
      "received_at_ms" => attrs["received_at_ms"]
    }
    |> drop_nil()
  end

  defp maybe_put_now(%{} = attrs, nil), do: attrs
  defp maybe_put_now(%{} = attrs, now_ms), do: Map.put_new(attrs, "received_at_ms", now_ms)

  defp normalize_execution(%{} = execution) do
    type = required_string!(execution, "type")

    execution
    |> Map.take(["type", "pipeline", "capability_ref", "internal"])
    |> Map.put("type", type)
  end

  defp public_unit(unit) do
    unit
    |> Map.take([
      "id",
      "scope",
      "tenant",
      "schedule",
      "execution",
      "pool",
      "priority",
      "concurrency",
      "health",
      "circuit_breaker",
      "conditions",
      "restart_policy",
      "part_of",
      "wants",
      "requires",
      "conflicts_with",
      "before",
      "after"
    ])
    |> drop_nil()
  end

  defp normalize_concurrency(%{} = concurrency) do
    if_running = string_or(concurrency["if_running"], "queue")

    %{}
    |> maybe_put("max_instances", positive_int_or_nil(concurrency["max_instances"]))
    |> maybe_put(
      "if_running",
      if(if_running in @if_running_values, do: if_running, else: "queue")
    )
    |> maybe_put("queue_max", non_negative_int_or_nil(concurrency["queue_max"]))
    |> maybe_put("priority", int_or(concurrency["priority"], nil))
  end

  defp normalize_health(nil), do: %{}
  defp normalize_health(value) when not is_map(value), do: %{}

  defp normalize_health(%{} = health) do
    health = stringify(health)
    type = string_or(health["type"], "heartbeat")
    on_unhealthy = string_or(health["on_unhealthy"], "log")

    %{}
    |> maybe_put("type", if(type in @health_types, do: type, else: "heartbeat"))
    |> maybe_put(
      "on_unhealthy",
      if(on_unhealthy in @on_unhealthy_values, do: on_unhealthy, else: "log")
    )
    |> maybe_put("path", health["path"])
    |> maybe_put("command", health["command"])
    |> maybe_put("heartbeat_timeout_ms", positive_int_or_nil(health["heartbeat_timeout_ms"]))
  end

  defp normalize_circuit_breaker(nil), do: %{}
  defp normalize_circuit_breaker(value) when not is_map(value), do: %{}

  defp normalize_circuit_breaker(%{} = breaker) do
    breaker = stringify(breaker)

    %{}
    |> maybe_put("threshold", positive_int_or_nil(breaker["threshold"]))
    |> maybe_put("cooldown_ms", non_negative_int_or_nil(breaker["cooldown_ms"]))
  end

  defp normalize_restart_policy(nil), do: %{"action" => "skip"}

  defp normalize_restart_policy(action) when is_binary(action) do
    %{"action" => if(action in @restart_actions, do: action, else: "skip")}
  end

  defp normalize_restart_policy(%{} = policy) do
    policy = stringify(policy)
    action = string_or(policy["action"] || policy["strategy"] || policy["policy"], "skip")

    %{
      "action" => if(action in @restart_actions, do: action, else: "skip")
    }
    |> maybe_put("max_retries", non_negative_int_or_nil(policy["max_retries"]))
    |> maybe_put(
      "backoff_ms",
      non_negative_int_or_nil(policy["backoff_ms"] || policy["initial_backoff_ms"])
    )
    |> maybe_put("backoff_multiplier", float_or(policy["backoff_multiplier"], nil))
    |> maybe_put("max_backoff_ms", non_negative_int_or_nil(policy["max_backoff_ms"]))
  end

  defp normalize_restart_policy(_policy), do: %{"action" => "skip"}

  defp normalize_conditions(nil), do: []

  defp normalize_conditions(value) when is_list(value),
    do: Enum.map(value, &normalize_condition/1)

  defp normalize_conditions(%{} = value), do: [normalize_condition(value)]
  defp normalize_conditions(_value), do: []

  defp normalize_condition(%{} = condition) do
    condition = stringify(condition)
    type = string_or(condition["type"], infer_condition_type(condition))

    %{
      "id" => string_or(condition["id"], default_condition_id(condition, type)),
      "type" => if(type in @condition_types, do: type, else: "command"),
      "path" => string_or(condition["path"], nil),
      "name" => string_or(condition["name"] || condition["env"], nil),
      "value" => condition["value"],
      "command" => string_or(condition["command"] || condition["cmd"], nil)
    }
    |> drop_nil()
  end

  defp normalize_condition(_condition), do: %{"id" => "invalid", "type" => "command"}

  defp infer_condition_type(%{"path" => _path}), do: "file_exists"
  defp infer_condition_type(%{"env" => _env, "value" => _value}), do: "env_equals"
  defp infer_condition_type(%{"env" => _env}), do: "env_present"
  defp infer_condition_type(%{"name" => _name, "value" => _value}), do: "env_equals"
  defp infer_condition_type(%{"name" => _name}), do: "env_present"
  defp infer_condition_type(_condition), do: "command"

  defp default_condition_id(condition, type) do
    key =
      condition["id"] ||
        condition["path"] ||
        condition["name"] ||
        condition["env"] ||
        condition["command"] ||
        condition["cmd"] ||
        type

    "#{type}:#{safe_id(key)}"
  end

  defp health_result(unit, attrs, now_ms) do
    attrs = stringify(attrs)
    health = map_or(unit["health"], %{})
    type = string_or(attrs["type"], string_or(health["type"], "heartbeat"))
    status = string_or(attrs["status"], "unknown")

    %{
      "type" => if(type in @health_types, do: type, else: "heartbeat"),
      "status" => if(status in @health_statuses, do: status, else: "unknown"),
      "observed_at_ms" => int_or(attrs["observed_at_ms"], now_ms),
      "message" => string_or(attrs["message"], nil)
    }
    |> maybe_put("path", attrs["path"] || health["path"])
    |> maybe_put("command", attrs["command"] || health["command"])
    |> drop_nil()
  end

  defp admit_trigger(
         %Trace{} = trace,
         unit,
         trigger,
         task,
         service_state,
         pool_state,
         now_ms,
         caused_by,
         opts
       ) do
    concurrency = map_or(unit["concurrency"], %{})
    max_instances = concurrency["max_instances"]
    if_running = string_or(concurrency["if_running"], "queue")
    queue_max = concurrency["queue_max"]
    service_tasks = service_tasks(pool_state, unit["id"])
    running_tasks = Enum.filter(service_tasks, &(&1["status"] in @running_statuses))
    queued_count = Enum.count(service_tasks, &(&1["status"] == "queued"))

    snapshot = %{
      "max_instances" => max_instances,
      "if_running" => if_running,
      "queue_max" => queue_max,
      "running" => length(running_tasks),
      "queued" => queued_count
    }

    with {:ok, trace} <- admit_conditions(trace, unit, trigger, task, caused_by, opts),
         {:ok, trace} <- admit_circuit(trace, unit, service_state, now_ms, caused_by) do
      cond do
        is_nil(max_instances) ->
          {:enqueue, trace}

        length(running_tasks) < max_instances ->
          {:enqueue, trace}

        if_running == "skip" ->
          {:skip, trace, "already_running", snapshot}

        if_running == "kill" ->
          {:enqueue,
           append_kill_requests(trace, unit, trigger, task, service_tasks, snapshot, caused_by)}

        queue_full?(queue_max, queued_count) ->
          {:skip, trace, "queue_full", snapshot}

        true ->
          {:enqueue, trace}
      end
    end
  end

  defp admit_conditions(trace, unit, trigger, task, caused_by, opts) do
    conditions = list_or(unit["conditions"])

    if conditions == [] do
      {:ok, trace}
    else
      results = Enum.map(conditions, &evaluate_condition(&1, opts))
      passed? = Enum.all?(results, &(&1["passed?"] == true))

      trace =
        Trace.append_effect(trace, %{
          type: "service_conditions_checked",
          caused_by: caused_by,
          ref: %{"service_id" => unit["id"], "trigger_type" => trigger["type"]},
          payload: %{
            "unit" => public_unit(unit),
            "trigger" => trigger,
            "task_id" => task["id"],
            "passed?" => passed?,
            "results" => results
          }
        })

      if passed? do
        {:ok, trace}
      else
        snapshot = %{"conditions" => results}
        {:skip, trace, "conditions_failed", snapshot}
      end
    end
  end

  defp evaluate_condition(%{} = condition, opts) do
    passed? =
      case condition["type"] do
        "file_exists" ->
          path = condition["path"]
          is_binary(path) and File.exists?(path)

        "env_present" ->
          condition_env(opts, condition["name"]) not in [nil, ""]

        "env_equals" ->
          condition_env(opts, condition["name"]) == to_string(condition["value"])

        "command" ->
          command_condition_result(condition, opts)

        _type ->
          false
      end

    %{
      "id" => condition["id"],
      "type" => condition["type"],
      "passed?" => passed?
    }
    |> maybe_put("path", condition["path"])
    |> maybe_put("name", condition["name"])
    |> maybe_put("command", condition["command"])
  end

  defp condition_env(opts, name) when is_binary(name) do
    env = Keyword.get(opts, :env, %{})

    case env do
      %{} -> Map.get(env, name) || existing_atom_env(env, name)
      _env -> nil
    end
  end

  defp condition_env(_opts, _name), do: nil

  defp existing_atom_env(env, name) do
    Map.get(env, String.to_existing_atom(name))
  rescue
    ArgumentError -> nil
  end

  defp command_condition_result(condition, opts) do
    evaluator = Keyword.get(opts, :condition_evaluator)

    cond do
      is_function(evaluator, 1) ->
        truthy?(evaluator.(condition))

      is_function(evaluator, 2) ->
        truthy?(evaluator.(condition["command"], condition))

      true ->
        condition_results = stringify(Keyword.get(opts, :condition_results, %{}))
        truthy?(condition_results[condition["id"]] || condition_results[condition["command"]])
    end
  end

  defp admit_circuit(trace, unit, service_state, now_ms, caused_by) do
    service = get_in(service_state, ["services", unit["id"]]) || %{}
    breaker = map_or(unit["circuit_breaker"], %{})
    state = service["circuit_state"] || "closed"
    opened_at_ms = service["circuit_opened_at_ms"]
    cooldown_ms = int_or(breaker["cooldown_ms"], 0)

    cond do
      state != "open" ->
        {:ok, trace}

      is_integer(opened_at_ms) and now_ms - opened_at_ms >= cooldown_ms ->
        {:ok,
         append_circuit_transition(
           trace,
           unit,
           "half-open",
           now_ms,
           "cooldown_elapsed",
           caused_by
         )}

      true ->
        snapshot = %{
          "circuit_state" => "open",
          "opened_at_ms" => opened_at_ms,
          "cooldown_ms" => cooldown_ms,
          "now_ms" => now_ms
        }

        {:skip, trace, "circuit_open", snapshot}
    end
  end

  defp append_restart_decision(trace, unit, event, service_state, now_ms, caused_by) do
    policy = map_or(unit["restart_policy"], %{"action" => "skip"})
    action = string_or(policy["action"], "skip")
    service_id = unit["id"]
    failed_task_id = failed_task_id(event)
    restart_count = restart_count(service_state, service_id)
    attempt = restart_count + 1

    cond do
      restart_already_handled?(service_state, service_id, failed_task_id) ->
        {trace, nil}

      action == "retry" and retry_allowed?(attempt, policy) ->
        schedule = restart_schedule(unit, event, failed_task_id, attempt, policy, now_ms)

        trace =
          Trace.append_effect(trace, %{
            type: "service_restart_scheduled",
            caused_by: caused_by,
            ref: %{
              "service_id" => service_id,
              "restart_id" => schedule["id"],
              "failed_task_id" => failed_task_id
            },
            payload: %{"schedule" => schedule}
          })

        {trace, schedule}

      action == "retry" ->
        {append_restart_skipped(trace, unit, event, "max_retries_exceeded", caused_by),
         %{"service_id" => service_id, "action" => "skip", "reason" => "max_retries_exceeded"}}

      action == "abort" ->
        trace =
          Trace.append_effect(trace, %{
            type: "service_aborted",
            caused_by: caused_by,
            ref: %{"service_id" => service_id, "failed_task_id" => failed_task_id},
            payload: %{"event" => event, "reason" => "restart_policy_abort"}
          })

        {trace, %{"service_id" => service_id, "action" => "abort"}}

      true ->
        {append_restart_skipped(trace, unit, event, "restart_policy_skip", caused_by),
         %{"service_id" => service_id, "action" => "skip", "reason" => "restart_policy_skip"}}
    end
  end

  defp append_restart_skipped(trace, unit, event, reason, caused_by) do
    Trace.append_effect(trace, %{
      type: "service_restart_skipped",
      caused_by: caused_by,
      ref: %{"service_id" => unit["id"], "failed_task_id" => failed_task_id(event)},
      payload: %{"event" => event, "reason" => reason}
    })
  end

  defp retry_allowed?(attempt, policy) do
    case policy["max_retries"] do
      max when is_integer(max) -> attempt <= max
      _no_limit -> true
    end
  end

  defp restart_schedule(unit, event, failed_task_id, attempt, policy, now_ms) do
    backoff_ms = restart_backoff_ms(attempt, policy)

    %{
      "id" => "#{unit["id"]}:restart:#{safe_id(failed_task_id)}:#{attempt}",
      "service_id" => unit["id"],
      "failed_task_id" => failed_task_id,
      "attempt" => attempt,
      "due_at_ms" => now_ms + backoff_ms,
      "backoff_ms" => backoff_ms,
      "event" => event
    }
  end

  defp restart_backoff_ms(attempt, policy) do
    base = int_or(policy["backoff_ms"], 0)
    multiplier = float_or(policy["backoff_multiplier"], 2.0)
    max_backoff = policy["max_backoff_ms"]

    backoff =
      base
      |> Kernel.*(:math.pow(multiplier, max(attempt - 1, 0)))
      |> round()

    if is_integer(max_backoff), do: min(backoff, max_backoff), else: backoff
  end

  defp failed_service_id(%{"type" => "service.failed:" <> service_id}), do: service_id
  defp failed_service_id(_event), do: nil

  defp failed_task_id(event) do
    get_in(event, ["ref", "task_id"]) ||
      get_in(event, ["payload", "task_id"]) ||
      get_in(event, ["payload", "detail", "task_id"]) ||
      "unknown"
  end

  defp restart_count(service_state, service_id) do
    get_in(service_state, ["services", service_id, "restart_count"]) || 0
  end

  defp restart_already_handled?(service_state, service_id, failed_task_id) do
    service_state
    |> get_in(["services", service_id, "restart_schedules"])
    |> map_or(%{})
    |> Map.values()
    |> Enum.any?(&(&1["failed_task_id"] == failed_task_id))
  end

  defp due_restart_schedules(service_state, service_id, now_ms) do
    service_state
    |> get_in(["services", service_id, "restart_schedules"])
    |> map_or(%{})
    |> Map.values()
    |> Enum.filter(fn schedule ->
      schedule["status"] == "scheduled" and
        is_integer(schedule["due_at_ms"]) and
        schedule["due_at_ms"] <= now_ms
    end)
    |> Enum.sort_by(&{&1["due_at_ms"], &1["id"]})
  end

  defp append_trigger_skipped(trace, unit, trigger, task, reason, snapshot, caused_by) do
    Trace.append_effect(trace, %{
      type: "service_trigger_skipped",
      caused_by: caused_by,
      ref: %{
        "service_id" => unit["id"],
        "trigger_type" => trigger["type"],
        "task_id" => task["id"]
      },
      payload: %{
        "unit" => public_unit(unit),
        "trigger" => trigger,
        "task" => task,
        "reason" => reason,
        "admission" => snapshot,
        "concurrency" => snapshot
      }
    })
  end

  defp append_unhealthy_action(trace, unit, check, service_state, pool_state, caused_by) do
    action = string_or(get_in(unit, ["health", "on_unhealthy"]), "log")
    service_tasks = service_tasks(pool_state, unit["id"])

    active_task_ids =
      service_tasks
      |> Enum.filter(&(&1["status"] in ["queued" | @running_statuses]))
      |> Enum.map(& &1["id"])
      |> Enum.reject(&is_nil/1)

    trace =
      Trace.append_effect(trace, %{
        type: "service_unhealthy_action",
        caused_by: caused_by,
        ref: %{"service_id" => unit["id"], "action" => action},
        payload: %{
          "check" => check,
          "action" => action,
          "task_ids" => active_task_ids
        }
      })

    trace =
      if action in ["kill", "restart"] do
        Enum.reduce(active_task_ids, trace, fn task_id, trace ->
          Mitos.WorkPool.append_status(trace, task_id, :cancelled,
            caused_by: caused_by,
            reason: "service health unhealthy"
          )
        end)
      else
        trace
      end

    if action == "restart" do
      append_health_restart_schedule(
        trace,
        unit,
        check,
        service_state,
        active_task_ids,
        caused_by
      )
    else
      trace
    end
  end

  defp append_health_restart_schedule(
         trace,
         unit,
         check,
         service_state,
         active_task_ids,
         caused_by
       ) do
    failed_task_id =
      active_task_ids
      |> List.first()
      |> case do
        nil -> "health:#{check["observed_at_ms"] || "unknown"}"
        task_id -> task_id
      end

    if restart_already_handled?(service_state, unit["id"], failed_task_id) do
      trace
    else
      policy = map_or(unit["restart_policy"], %{})
      attempt = restart_count(service_state, unit["id"]) + 1
      policy = if policy["action"] == "retry", do: policy, else: %{"backoff_ms" => 0}

      schedule =
        %{
          "id" => "#{unit["id"]}:health-restart:#{safe_id(failed_task_id)}:#{attempt}",
          "service_id" => unit["id"],
          "failed_task_id" => failed_task_id,
          "attempt" => attempt,
          "due_at_ms" => check["observed_at_ms"] + restart_backoff_ms(attempt, policy),
          "backoff_ms" => restart_backoff_ms(attempt, policy),
          "reason" => "health_unhealthy",
          "check" => check
        }

      Trace.append_effect(trace, %{
        type: "service_restart_scheduled",
        caused_by: caused_by,
        ref: %{
          "service_id" => unit["id"],
          "restart_id" => schedule["id"],
          "failed_task_id" => failed_task_id
        },
        payload: %{"schedule" => schedule}
      })
    end
  end

  defp maybe_open_circuit(trace, unit, service_state, check, caused_by) do
    breaker = map_or(unit["circuit_breaker"], %{})
    threshold = breaker["threshold"]
    service = get_in(service_state, ["services", unit["id"]]) || %{}
    next_streak = int_or(service["health_failure_streak"], 0) + 1

    cond do
      is_nil(threshold) ->
        trace

      next_streak < threshold ->
        trace

      service["circuit_state"] == "open" ->
        trace

      true ->
        append_circuit_transition(
          trace,
          unit,
          "open",
          check["observed_at_ms"],
          "health_threshold",
          caused_by
        )
    end
  end

  defp maybe_close_circuit(trace, unit, service_state, check, caused_by) do
    service = get_in(service_state, ["services", unit["id"]]) || %{}

    if service["circuit_state"] in ["open", "half-open"] do
      append_circuit_transition(
        trace,
        unit,
        "closed",
        check["observed_at_ms"],
        "health_recovered",
        caused_by
      )
    else
      trace
    end
  end

  defp append_circuit_transition(trace, unit, state, now_ms, reason, caused_by) do
    Trace.append_effect(trace, %{
      type: "service_circuit_#{String.replace(state, "-", "_")}",
      caused_by: caused_by,
      ref: %{"service_id" => unit["id"], "state" => state},
      payload: %{
        "state" => state,
        "transitioned_at_ms" => now_ms,
        "reason" => reason,
        "circuit_breaker" => map_or(unit["circuit_breaker"], %{})
      }
    })
  end

  defp append_kill_requests(trace, unit, trigger, task, service_tasks, snapshot, caused_by) do
    killed_tasks =
      service_tasks
      |> Enum.filter(&(&1["status"] in ["queued" | @running_statuses]))
      |> Enum.map(& &1["id"])
      |> Enum.reject(&is_nil/1)

    trace =
      Trace.append_effect(trace, %{
        type: "service_instances_killed",
        caused_by: caused_by,
        ref: %{
          "service_id" => unit["id"],
          "trigger_type" => trigger["type"],
          "task_id" => task["id"]
        },
        payload: %{
          "replacement_task" => task,
          "killed_task_ids" => killed_tasks,
          "concurrency" => snapshot
        }
      })

    Enum.reduce(killed_tasks, trace, fn task_id, trace ->
      Mitos.WorkPool.append_status(trace, task_id, :cancelled,
        caused_by: caused_by,
        reason: "service if_running=kill"
      )
    end)
  end

  defp queue_full?(queue_max, queued_count)
       when is_integer(queue_max) and queue_max >= 0 and queued_count >= queue_max,
       do: true

  defp queue_full?(_queue_max, _queued_count), do: false

  defp service_tasks(pool_state, service_id) do
    pool_state
    |> Map.get("tasks", %{})
    |> Map.values()
    |> Enum.filter(&(task_service_id(&1) == service_id))
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

  defp pool_for(unit, %{"type" => "internal"}), do: string_or(unit["pool"], "platform:internal")
  defp pool_for(unit, _execution), do: string_or(unit["pool"], "services")

  defp default_task_id(unit, trigger) do
    key =
      trigger["idempotency_key"] ||
        trigger["delivery_id"] ||
        trigger["event_type"] ||
        trigger["received_at"] ||
        Integer.to_string(System.unique_integer([:positive]))

    "svc:#{unit["id"]}:#{trigger["type"]}:#{safe_id(key)}"
  end

  defp external_ref(%{"external_ref" => %{} = external}), do: external

  defp external_ref(%{"delivery_id" => delivery_id}) when is_binary(delivery_id),
    do: %{"ref" => delivery_id}

  defp external_ref(_trigger), do: nil

  defp active_units(%{"active" => %{} = active}) do
    active
    |> Map.values()
    |> Enum.flat_map(fn
      %{"unit" => %{} = unit} -> [unit]
      _row -> []
    end)
  end

  defp active_units(units) when is_list(units), do: Enum.filter(units, &is_map/1)
  defp active_units(_), do: []

  defp due_trigger(unit, service_state, pool_state, now_ms) do
    schedule = map_or(unit["schedule"], %{"type" => "manual"})

    case schedule["type"] do
      "interval" ->
        interval_trigger(unit, schedule, service_state, now_ms)

      "cron" ->
        interval_trigger(unit, schedule, service_state, now_ms)

      "continuous" ->
        continuous_trigger(unit, schedule, service_state, pool_state, now_ms)

      _other ->
        nil
    end
  end

  defp interval_trigger(unit, schedule, service_state, now_ms) do
    interval_ms = int_or(schedule["every_ms"], int_or(schedule["interval_ms"], 0))
    last_ms = last_trigger_attempt_ms(service_state, unit["id"])

    cond do
      interval_ms <= 0 ->
        nil

      is_nil(last_ms) or now_ms - last_ms >= interval_ms ->
        %{
          type: schedule["type"],
          idempotency_key: "#{unit["id"]}:#{schedule["type"]}:#{now_ms}",
          inputs: map_or(schedule["inputs"], %{}),
          received_at_ms: now_ms
        }

      true ->
        nil
    end
  end

  defp continuous_trigger(unit, schedule, service_state, pool_state, now_ms) do
    restart_delay_ms = int_or(schedule["restart_delay_ms"], int_or(unit["restart_delay_ms"], 0))
    last_ms = last_trigger_attempt_ms(service_state, unit["id"])

    cond do
      active_service_task?(pool_state, unit["id"]) ->
        nil

      is_integer(last_ms) and now_ms - last_ms < restart_delay_ms ->
        nil

      true ->
        %{
          type: "continuous",
          idempotency_key: "#{unit["id"]}:continuous:#{now_ms}",
          inputs: map_or(schedule["inputs"], %{}),
          received_at_ms: now_ms
        }
    end
  end

  defp active_service_task?(pool_state, service_id) do
    pool_state
    |> Map.get("tasks", %{})
    |> Map.values()
    |> Enum.any?(fn task ->
      task["source"] == "service:#{service_id}:continuous" and
        task["status"] in ["queued", "scheduled", "running", "parked"]
    end)
  end

  defp last_trigger_attempt_ms(service_state, service_id) do
    [
      get_in(service_state, ["services", service_id, "last_trigger_attempt_at_ms"]),
      get_in(service_state, ["services", service_id, "last_triggered_at_ms"]),
      get_in(service_state, ["services", service_id, "last_skipped_at_ms"])
    ]
    |> Enum.filter(&is_integer/1)
    |> Enum.max(fn -> nil end)
  end

  defp event_trigger_matches?(unit, event) do
    schedule = map_or(unit["schedule"], %{})

    schedule["type"] == "event" and
      event_type(event) in event_patterns(schedule)
  end

  defp event_patterns(schedule) do
    ([
       schedule["event_type"],
       schedule["event"],
       schedule["on"],
       schedule["match"]
     ] ++ list_or(schedule["events"]))
    |> Enum.reject(&is_nil/1)
    |> Enum.map(&to_string/1)
  end

  defp event_type(%{"kind" => "effect", "type" => type}) when is_binary(type), do: type
  defp event_type(%{"type" => type}) when is_binary(type), do: type
  defp event_type(%{"event" => %{"type" => type}}) when is_binary(type), do: type
  defp event_type(_event), do: "unknown"

  defp event_idempotency_key(unit, event) do
    seq = event["seq"] || event["sequence"] || get_in(event, ["event", "seq"])
    run_id = event["run_id"] || get_in(event, ["event", "run_id"]) || "unknown"
    "#{unit["id"]}:event:#{event_type(event)}:#{run_id}:#{seq || "none"}"
  end

  defp canonical_effect(%{"kind" => "effect"} = event), do: [event]
  defp canonical_effect(%{"event" => %{"kind" => "effect"} = event}), do: [event]
  defp canonical_effect(_event), do: []

  defp initial_service_state, do: %{"services" => %{}, "_task_index" => %{}}

  defp apply_service_effect(%{"type" => "service_triggered"} = effect, state) do
    ref = map_or(effect["ref"], %{})
    payload = map_or(effect["payload"], %{})
    trigger = map_or(payload["trigger"], %{})
    service_id = ref["service_id"]
    task_id = ref["task_id"]

    state
    |> put_task_service_index(task_id, service_id)
    |> update_in(["services", service_id], fn service ->
      service = service || %{"trigger_count" => 0, "triggers" => []}

      service
      |> Map.put("service_id", service_id)
      |> Map.put("status", "queued")
      |> Map.put("last_task_id", ref["task_id"])
      |> Map.put("last_trigger_type", ref["trigger_type"])
      |> Map.put("last_triggered_at_ms", trigger["received_at_ms"])
      |> Map.put("last_trigger_attempt_at_ms", trigger["received_at_ms"])
      |> Map.update("trigger_count", 1, &(&1 + 1))
      |> Map.update("triggers", [trigger], &(&1 ++ [trigger]))
    end)
  end

  defp apply_service_effect(%{"type" => "service_trigger_skipped"} = effect, state) do
    ref = map_or(effect["ref"], %{})
    payload = map_or(effect["payload"], %{})
    trigger = map_or(payload["trigger"], %{})
    service_id = ref["service_id"]

    update_in(state, ["services", service_id], fn service ->
      service = service || %{"trigger_count" => 0, "skipped_count" => 0, "triggers" => []}

      service
      |> Map.put("service_id", service_id)
      |> Map.put("status", "skipped")
      |> Map.put("last_task_id", ref["task_id"])
      |> Map.put("last_trigger_type", ref["trigger_type"])
      |> Map.put("last_skip_reason", payload["reason"])
      |> Map.put("last_skipped_at_ms", trigger["received_at_ms"])
      |> Map.put("last_trigger_attempt_at_ms", trigger["received_at_ms"])
      |> Map.update("skipped_count", 1, &(&1 + 1))
      |> Map.update("triggers", [trigger], &(&1 ++ [trigger]))
    end)
  end

  defp apply_service_effect(%{"type" => "service_conditions_checked"} = effect, state) do
    ref = map_or(effect["ref"], %{})
    payload = map_or(effect["payload"], %{})
    service_id = ref["service_id"]

    update_in(state, ["services", service_id], fn service ->
      service = service || %{"service_id" => service_id}

      service
      |> Map.put("service_id", service_id)
      |> Map.put(
        "conditions_status",
        if(payload["passed?"] == true, do: "passed", else: "failed")
      )
      |> Map.put("last_conditions", list_or(payload["results"]))
    end)
  end

  defp apply_service_effect(%{"type" => "service_instances_killed"} = effect, state) do
    ref = map_or(effect["ref"], %{})
    payload = map_or(effect["payload"], %{})
    service_id = ref["service_id"]

    update_in(state, ["services", service_id], fn service ->
      service = service || %{"kill_count" => 0, "killed_task_ids" => []}

      service
      |> Map.put("service_id", service_id)
      |> Map.put("status", "killed")
      |> Map.put("last_kill_task_id", ref["task_id"])
      |> Map.update("kill_count", 1, &(&1 + 1))
      |> Map.update(
        "killed_task_ids",
        list_or(payload["killed_task_ids"]),
        &Enum.uniq(&1 ++ list_or(payload["killed_task_ids"]))
      )
    end)
  end

  defp apply_service_effect(%{"type" => "service_health_checked"} = effect, state) do
    ref = map_or(effect["ref"], %{})
    payload = map_or(effect["payload"], %{})
    check = map_or(payload["check"], %{})
    service_id = ref["service_id"]

    update_in(state, ["services", service_id], fn service ->
      service = service || %{"service_id" => service_id}

      service
      |> Map.put("service_id", service_id)
      |> Map.put("health_status", check["status"])
      |> Map.put("last_health_check", check)
      |> Map.put("last_health_check_at_ms", check["observed_at_ms"])
      |> update_health_streak(check["status"])
    end)
  end

  defp apply_service_effect(%{"type" => "service_unhealthy_action"} = effect, state) do
    ref = map_or(effect["ref"], %{})
    payload = map_or(effect["payload"], %{})
    service_id = ref["service_id"]

    update_in(state, ["services", service_id], fn service ->
      service = service || %{"service_id" => service_id}

      service
      |> Map.put("service_id", service_id)
      |> Map.put("last_unhealthy_action", payload["action"])
      |> Map.put("last_unhealthy_action_task_ids", list_or(payload["task_ids"]))
    end)
  end

  defp apply_service_effect(%{"type" => "service_circuit_open"} = effect, state) do
    apply_circuit_transition(effect, state, "open")
  end

  defp apply_service_effect(%{"type" => "service_circuit_half_open"} = effect, state) do
    apply_circuit_transition(effect, state, "half-open")
  end

  defp apply_service_effect(%{"type" => "service_circuit_closed"} = effect, state) do
    apply_circuit_transition(effect, state, "closed")
  end

  defp apply_service_effect(%{"type" => "service_restart_scheduled"} = effect, state) do
    ref = map_or(effect["ref"], %{})
    payload = map_or(effect["payload"], %{})
    schedule = map_or(payload["schedule"], %{})
    service_id = ref["service_id"]

    update_in(state, ["services", service_id], fn service ->
      service = service || %{"service_id" => service_id}

      service
      |> Map.put("service_id", service_id)
      |> Map.put("last_restart_id", schedule["id"])
      |> Map.update("restart_count", 1, &(&1 + 1))
      |> put_restart_schedule(schedule, "scheduled")
    end)
  end

  defp apply_service_effect(%{"type" => "service_restart_fired"} = effect, state) do
    ref = map_or(effect["ref"], %{})
    payload = map_or(effect["payload"], %{})
    schedule = map_or(payload["schedule"], %{})
    service_id = ref["service_id"]

    update_in(state, ["services", service_id], fn service ->
      service = service || %{"service_id" => service_id}

      service
      |> Map.put("service_id", service_id)
      |> Map.put("last_restart_fired_id", ref["restart_id"])
      |> put_restart_schedule(schedule, "fired")
    end)
  end

  defp apply_service_effect(%{"type" => "service_restart_skipped"} = effect, state) do
    ref = map_or(effect["ref"], %{})
    payload = map_or(effect["payload"], %{})
    service_id = ref["service_id"]

    update_in(state, ["services", service_id], fn service ->
      service = service || %{"service_id" => service_id}

      service
      |> Map.put("service_id", service_id)
      |> Map.put("last_restart_skip_reason", payload["reason"])
      |> Map.update("restart_skipped_count", 1, &(&1 + 1))
    end)
  end

  defp apply_service_effect(%{"type" => "service_aborted"} = effect, state) do
    ref = map_or(effect["ref"], %{})
    payload = map_or(effect["payload"], %{})
    service_id = ref["service_id"]

    update_in(state, ["services", service_id], fn service ->
      service = service || %{"service_id" => service_id}

      service
      |> Map.put("service_id", service_id)
      |> Map.put("status", "aborted")
      |> Map.put("last_abort_reason", payload["reason"])
      |> Map.update("abort_count", 1, &(&1 + 1))
    end)
  end

  defp apply_service_effect(%{"type" => "task_running"} = effect, state) do
    apply_task_status_effect(effect, state, "running")
  end

  defp apply_service_effect(%{"type" => "task_done"} = effect, state) do
    apply_task_status_effect(effect, state, "succeeded")
  end

  defp apply_service_effect(%{"type" => "task_failed"} = effect, state) do
    apply_task_status_effect(effect, state, "failed")
  end

  defp apply_service_effect(%{"type" => "task_cancelled"} = effect, state) do
    apply_task_status_effect(effect, state, "cancelled")
  end

  defp apply_service_effect(%{"type" => "service.succeeded:" <> service_id} = effect, state) do
    apply_service_outcome_effect(effect, state, service_id, "succeeded")
  end

  defp apply_service_effect(%{"type" => "service.failed:" <> service_id} = effect, state) do
    apply_service_outcome_effect(effect, state, service_id, "failed")
  end

  defp apply_service_effect(%{"type" => "service.finished:" <> service_id} = effect, state) do
    apply_service_outcome_effect(effect, state, service_id, "finished")
  end

  defp apply_service_effect(_effect, state), do: state

  defp update_health_streak(service, "healthy") do
    service
    |> Map.put("health_failure_streak", 0)
    |> Map.update("health_success_count", 1, &(&1 + 1))
  end

  defp update_health_streak(service, "unhealthy") do
    service
    |> Map.update("health_failure_streak", 1, &(&1 + 1))
    |> Map.update("health_failure_count", 1, &(&1 + 1))
  end

  defp update_health_streak(service, _status), do: service

  defp apply_circuit_transition(effect, state, circuit_state) do
    ref = map_or(effect["ref"], %{})
    payload = map_or(effect["payload"], %{})
    service_id = ref["service_id"]
    at_ms = payload["transitioned_at_ms"]

    update_in(state, ["services", service_id], fn service ->
      service = service || %{"service_id" => service_id}

      service
      |> Map.put("service_id", service_id)
      |> Map.put("circuit_state", circuit_state)
      |> maybe_put("last_circuit_reason", payload["reason"])
      |> put_circuit_timestamp(circuit_state, at_ms)
      |> reset_circuit_failure_streak(circuit_state)
    end)
  end

  defp put_circuit_timestamp(service, "open", at_ms),
    do: maybe_put(service, "circuit_opened_at_ms", at_ms)

  defp put_circuit_timestamp(service, "half-open", at_ms),
    do: maybe_put(service, "circuit_half_opened_at_ms", at_ms)

  defp put_circuit_timestamp(service, "closed", at_ms),
    do: maybe_put(service, "circuit_closed_at_ms", at_ms)

  defp reset_circuit_failure_streak(service, "closed"),
    do: Map.put(service, "health_failure_streak", 0)

  defp reset_circuit_failure_streak(service, _state), do: service

  defp put_restart_schedule(service, schedule, status) do
    schedule = Map.put(schedule, "status", status)

    update_in(service, ["restart_schedules"], fn schedules ->
      schedules = map_or(schedules, %{})
      Map.put(schedules, schedule["id"], schedule)
    end)
  end

  defp put_task_service_index(state, task_id, service_id)
       when is_binary(task_id) and is_binary(service_id) do
    put_in(state, ["_task_index", task_id], service_id)
  end

  defp put_task_service_index(state, _task_id, _service_id), do: state

  defp apply_task_status_effect(effect, state, status) do
    task_id = map_or(effect["ref"], %{})["task_id"]

    case get_in(state, ["_task_index", task_id]) do
      service_id when is_binary(service_id) ->
        update_service_task_status(state, service_id, task_id, effect, status)

      _nil ->
        state
    end
  end

  defp update_service_task_status(state, service_id, task_id, effect, status) do
    payload = map_or(effect["payload"], %{})

    update_in(state, ["services", service_id], fn service ->
      service = service || %{"service_id" => service_id}

      service
      |> Map.put("service_id", service_id)
      |> Map.put("status", status)
      |> Map.put("last_task_id", task_id)
      |> maybe_put("last_run_id", payload["run_id"])
      |> maybe_increment_task_status_count(status)
    end)
  end

  defp apply_service_outcome_effect(effect, state, service_id, outcome) do
    ref = map_or(effect["ref"], %{})
    payload = map_or(effect["payload"], %{})

    update_in(state, ["services", service_id], fn service ->
      service = service || %{"service_id" => service_id}

      service
      |> Map.put("service_id", service_id)
      |> put_outcome_status(outcome)
      |> put_outcome_refs(outcome, ref, payload)
      |> maybe_increment_outcome_count(outcome)
    end)
  end

  defp put_outcome_status(service, "finished"), do: service
  defp put_outcome_status(service, outcome), do: Map.put(service, "status", outcome)

  defp put_outcome_refs(service, "finished", ref, payload) do
    service
    |> maybe_put("last_finished_task_id", ref["task_id"])
    |> maybe_put("last_finished_run_id", payload["run_id"])
  end

  defp put_outcome_refs(service, _outcome, ref, payload) do
    service
    |> maybe_put("last_task_id", ref["task_id"])
    |> maybe_put("last_run_id", payload["run_id"])
  end

  defp maybe_increment_task_status_count(service, "running"),
    do: Map.update(service, "run_count", 1, &(&1 + 1))

  defp maybe_increment_task_status_count(service, "cancelled"),
    do: Map.update(service, "cancelled_count", 1, &(&1 + 1))

  defp maybe_increment_task_status_count(service, _status), do: service

  defp maybe_increment_outcome_count(service, "succeeded"),
    do: Map.update(service, "success_count", 1, &(&1 + 1))

  defp maybe_increment_outcome_count(service, "failed"),
    do: Map.update(service, "failure_count", 1, &(&1 + 1))

  defp maybe_increment_outcome_count(service, "finished"),
    do: Map.update(service, "finished_count", 1, &(&1 + 1))

  defp maybe_increment_outcome_count(service, _outcome), do: service

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

  defp empty_resolution,
    do: %{"members" => %{}, "masked" => %{}, "failed" => %{}, "disabled" => []}

  defp apply_presets(resolution, presets, project_type, units) do
    Enum.reduce(presets, resolution, fn preset, resolution ->
      if preset["project_type"] == project_type do
        resolution
        |> add_unit_members(
          list_or(preset["enable"]),
          units,
          "preset:#{project_type} (enable)",
          :soft
        )
        |> apply_explicit_disables(list_or(preset["disable"]))
      else
        resolution
      end
    end)
  end

  defp apply_targets(resolution, target_ids, targets, units) do
    Enum.reduce(target_ids, resolution, fn target_id, resolution ->
      activate_target(resolution, target_id, targets, units, MapSet.new())
    end)
  end

  defp apply_pipelines(resolution, pipeline_ids, pipelines, targets, units) do
    Enum.reduce(pipeline_ids, resolution, fn pipeline_id, resolution ->
      case Map.fetch(pipelines, pipeline_id) do
        {:ok, pipeline} ->
          requires = map_or(pipeline["requires"], %{})

          resolution
          |> add_unit_members(
            list_or(requires["services"]),
            units,
            "pipeline:#{pipeline_id} (requires)",
            :hard
          )
          |> apply_targets(list_or(requires["targets"]), targets, units)

        :error ->
          resolution
      end
    end)
  end

  defp apply_explicit_enables(resolution, ids, units) do
    add_unit_members(resolution, ids, units, "explicit (enable)", :hard)
  end

  defp apply_explicit_disables(resolution, ids) do
    Enum.reduce(ids, resolution, fn id, resolution ->
      resolution
      |> update_in(["members"], &Map.delete(&1, id))
      |> update_in(["disabled"], &Enum.uniq([id | &1]))
    end)
  end

  defp activate_target(resolution, target_id, targets, units, seen) do
    cond do
      MapSet.member?(seen, target_id) ->
        resolution

      not Map.has_key?(targets, target_id) ->
        resolution

      true ->
        target = Map.fetch!(targets, target_id)
        seen = MapSet.put(seen, target_id)

        resolution
        |> add_target_members(
          list_or(target["requires"]),
          targets,
          units,
          "#{target_id} (requires)",
          :hard,
          seen
        )
        |> add_target_members(
          list_or(target["wants"]),
          targets,
          units,
          "#{target_id} (wants)",
          :soft,
          seen
        )
    end
  end

  defp add_target_members(resolution, ids, targets, units, why, strength, seen) do
    Enum.reduce(ids, resolution, fn id, resolution ->
      if Map.has_key?(targets, id) do
        activate_target(resolution, id, targets, units, seen)
      else
        add_unit_member(resolution, id, units, why, strength)
      end
    end)
  end

  defp add_unit_members(resolution, ids, units, why, strength) do
    Enum.reduce(ids, resolution, &add_unit_member(&2, &1, units, why, strength))
  end

  defp add_unit_member(resolution, id, units, why, strength) do
    if Map.has_key?(units, id) and not disabled?(resolution, id) do
      update_in(resolution, ["members", id], fn member ->
        member = member || %{"why" => [], "hard?" => false}

        member
        |> Map.update!("why", &Enum.uniq(&1 ++ [why]))
        |> Map.update!("hard?", &(&1 or strength == :hard))
      end)
    else
      resolution
    end
  end

  defp expand_unit_relations(resolution, units) do
    resolution
    |> Map.fetch!("members")
    |> Map.keys()
    |> Enum.sort()
    |> Enum.reduce(resolution, fn id, resolution ->
      expand_unit_relations_for(resolution, id, units, MapSet.new())
    end)
  end

  defp expand_unit_relations_for(resolution, id, units, seen) do
    cond do
      MapSet.member?(seen, id) ->
        resolution

      not Map.has_key?(resolution["members"], id) ->
        resolution

      not Map.has_key?(units, id) ->
        resolution

      true ->
        unit = Map.fetch!(units, id)
        seen = MapSet.put(seen, id)

        resolution =
          Enum.reduce(list_or(unit["requires"]), resolution, fn required_id, resolution ->
            cond do
              disabled?(resolution, required_id) ->
                fail_unit(resolution, id, "missing required #{required_id}")

              not Map.has_key?(units, required_id) ->
                fail_unit(resolution, id, "missing required #{required_id}")

              true ->
                resolution
                |> add_unit_member(required_id, units, "#{id} (requires)", :hard)
                |> expand_unit_relations_for(required_id, units, seen)
            end
          end)

        if Map.has_key?(resolution["members"], id) do
          Enum.reduce(list_or(unit["wants"]), resolution, fn wanted_id, resolution ->
            if Map.has_key?(units, wanted_id) and not disabled?(resolution, wanted_id) do
              resolution
              |> add_unit_member(wanted_id, units, "#{id} (wants)", :soft)
              |> expand_unit_relations_for(wanted_id, units, seen)
            else
              resolution
            end
          end)
        else
          resolution
        end
    end
  end

  defp disabled?(resolution, id), do: id in Map.get(resolution, "disabled", [])

  defp fail_unit(resolution, id, reason) do
    {member, resolution} = pop_in(resolution, ["members", id])

    put_in(resolution, ["failed", id], %{
      "why" => Map.get(member || %{}, "why", []),
      "failed_by" => reason
    })
  end

  defp resolve_unit_conflicts(resolution, units) do
    conflicts =
      resolution
      |> Map.fetch!("members")
      |> Map.keys()
      |> Enum.flat_map(fn id ->
        units
        |> Map.fetch!(id)
        |> Map.get("conflicts_with", [])
        |> list_or()
        |> Enum.filter(&Map.has_key?(resolution["members"], &1))
        |> Enum.map(&Enum.sort([id, &1]))
      end)
      |> Enum.uniq()

    Enum.reduce_while(conflicts, {:ok, resolution}, fn [left, right], {:ok, resolution} ->
      left_hard? = get_in(resolution, ["members", left, "hard?"]) == true
      right_hard? = get_in(resolution, ["members", right, "hard?"]) == true

      cond do
        left_hard? and right_hard? ->
          {:halt, {:error, {:activation_conflict, [left, right]}}}

        left_hard? ->
          {:cont, {:ok, mask_unit(resolution, right, "conflicts_with #{left}")}}

        right_hard? ->
          {:cont, {:ok, mask_unit(resolution, left, "conflicts_with #{right}")}}

        true ->
          [keep, masked] = Enum.sort([left, right])
          {:cont, {:ok, mask_unit(resolution, masked, "conflicts_with #{keep}")}}
      end
    end)
  end

  defp mask_unit(resolution, id, reason) do
    {member, resolution} = pop_in(resolution, ["members", id])

    put_in(resolution, ["masked", id], %{
      "why" => Map.get(member || %{}, "why", []),
      "masked_by" => reason
    })
  end

  defp active_set_view(resolution, units) do
    active =
      resolution
      |> Map.fetch!("members")
      |> Map.new(fn {id, member} -> {id, unit_view(id, "active", member, units)} end)

    masked =
      resolution
      |> Map.fetch!("masked")
      |> Map.new(fn {id, member} -> {id, unit_view(id, "masked", member, units)} end)

    failed =
      resolution
      |> Map.fetch!("failed")
      |> Map.new(fn {id, member} -> {id, unit_view(id, "failed", member, units)} end)

    %{
      "active" => active,
      "masked" => masked,
      "failed" => failed,
      "order" => topological_order(active, units)
    }
  end

  defp unit_view(id, state, member, units) do
    unit = Map.fetch!(units, id)

    %{
      "id" => id,
      "state" => state,
      "trigger" => trigger_label(unit),
      "why" => Map.get(member, "why", [])
    }
    |> maybe_put("masked_by", Map.get(member, "masked_by"))
    |> maybe_put("failed_by", Map.get(member, "failed_by"))
    |> maybe_put("unit", public_unit(unit))
  end

  defp topological_order(active, units) do
    active_ids = active |> Map.keys() |> Enum.sort()
    active_set = MapSet.new(active_ids)
    edges = Map.new(active_ids, &{&1, MapSet.new()})

    edges =
      Enum.reduce(active_ids, edges, fn id, edges ->
        unit = Map.fetch!(units, id)

        edges =
          unit
          |> Map.get("before", [])
          |> list_or()
          |> Enum.filter(&MapSet.member?(active_set, &1))
          |> Enum.reduce(edges, fn before_id, edges ->
            Map.update!(edges, id, &MapSet.put(&1, before_id))
          end)

        unit
        |> Map.get("after", [])
        |> list_or()
        |> Enum.filter(&MapSet.member?(active_set, &1))
        |> Enum.reduce(edges, fn after_id, edges ->
          Map.update!(edges, after_id, &MapSet.put(&1, id))
        end)
      end)

    ordered = topo_visit(edges, [])

    if length(ordered) == length(active_ids), do: ordered, else: active_ids
  end

  defp topo_visit(edges, ordered) do
    incoming =
      edges
      |> Enum.flat_map(fn {_from, tos} -> MapSet.to_list(tos) end)
      |> MapSet.new()

    ready =
      edges
      |> Map.keys()
      |> Enum.reject(&MapSet.member?(incoming, &1))
      |> Enum.sort()

    case ready do
      [] ->
        Enum.reverse(ordered)

      [id | _rest] ->
        edges
        |> Map.delete(id)
        |> topo_visit([id | ordered])
    end
  end

  defp trigger_label(%{"schedule" => %{"type" => "interval", "every_ms" => every_ms}})
       when is_integer(every_ms),
       do: "interval/#{every_ms}"

  defp trigger_label(%{"schedule" => %{"type" => type}}), do: type
  defp trigger_label(_unit), do: "manual"

  defp index_by_id(values) do
    values
    |> list_or()
    |> Enum.filter(&(is_map(&1) and is_binary(&1["id"]) and &1["id"] != ""))
    |> Map.new(&{&1["id"], &1})
  end

  defp valid_scope(scope) when scope in @scopes, do: :ok

  defp valid_scope(_scope),
    do: {:error, {:invalid_service_unit, "scope must be one of #{Enum.join(@scopes, ", ")}"}}

  defp valid_execution_type(type) when type in @execution_types, do: :ok

  defp valid_execution_type(_type),
    do:
      {:error,
       {:invalid_service_unit,
        "execution.type must be one of #{Enum.join(@execution_types, ", ")}"}}

  defp valid_tenancy_boundary("internal", "platform", true), do: :ok

  defp valid_tenancy_boundary("internal", _scope, _platform?),
    do:
      {:error,
       {:invalid_service_unit, "internal execution is reserved for platform service units"}}

  defp valid_tenancy_boundary(type, _scope, _platform?) when type in @tenant_execution_types,
    do: :ok

  defp safe_id(value) do
    ~r/[^A-Za-z0-9_.:-]/
    |> Regex.replace(to_string(value), "_")
    |> String.slice(0, 96)
  end

  defp required_string!(map, key) do
    value = map[key]

    if is_binary(value) and value != "" do
      value
    else
      raise ArgumentError, "#{key} must be a non-empty string"
    end
  end

  defp string_or(value, _default) when is_binary(value) and value != "", do: value
  defp string_or(nil, default), do: default
  defp string_or(value, _default) when is_atom(value), do: Atom.to_string(value)
  defp string_or(_value, default), do: default

  defp int_or(value, _default) when is_integer(value), do: value

  defp int_or(value, default) when is_binary(value) do
    case Integer.parse(value) do
      {int, ""} -> int
      _ -> default
    end
  end

  defp int_or(_value, default), do: default

  defp float_or(value, _default) when is_float(value), do: value
  defp float_or(value, _default) when is_integer(value), do: value * 1.0

  defp float_or(value, default) when is_binary(value) do
    case Float.parse(value) do
      {float, ""} -> float
      _ -> default
    end
  end

  defp float_or(_value, default), do: default

  defp truthy?(value) when value in [true, "true", "ok", "pass", "passed", 0], do: true
  defp truthy?(value) when is_integer(value), do: value == 0
  defp truthy?(_value), do: false

  defp positive_int_or_nil(value) do
    case int_or(value, nil) do
      int when is_integer(int) and int > 0 -> int
      _value -> nil
    end
  end

  defp non_negative_int_or_nil(value) do
    case int_or(value, nil) do
      int when is_integer(int) and int >= 0 -> int
      _value -> nil
    end
  end

  defp list_or(value) when is_list(value), do: Enum.map(value, &stringify/1)
  defp list_or(_value), do: []

  defp map_or(%{} = map, _default), do: stringify(map)
  defp map_or(_value, default), do: default

  defp maybe_put(map, _key, nil), do: map
  defp maybe_put(map, key, value), do: Map.put(map, key, value)

  defp stringify(%{} = map),
    do: Map.new(map, fn {key, value} -> {to_string(key), stringify(value)} end)

  defp stringify(list) when is_list(list), do: Enum.map(list, &stringify/1)
  defp stringify(value), do: value

  defp drop_nil(map) do
    map
    |> Enum.reject(fn {_key, value} -> is_nil(value) end)
    |> Map.new()
  end
end
