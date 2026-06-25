defmodule Mitos.ServiceRuntimeTest do
  use ExUnit.Case, async: true

  test "service trigger appends a durable task into the work pool" do
    base_dir = unique_tmp_dir("service-runtime")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-service-runtime")

    {trace, task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        %{
          id: "webhook-listener",
          scope: "project",
          tenant: %{org: "org-a", project: "proj-a"},
          schedule: %{type: "webhook", path: "/capabilities/resize/runs"},
          execution: %{type: "pipeline", pipeline: "resize-and-deliver"},
          pool: "paid:default",
          priority: 8,
          requires: ["broker-warm"],
          conflicts_with: ["local-poll"]
        },
        %{
          type: "webhook",
          delivery_id: "evt-123",
          inputs: %{image: "artifact:input.png"}
        },
        caused_by: 0
      )

    assert task["id"] == "svc:webhook-listener:webhook:evt-123"
    assert task["source"] == "service:webhook-listener:webhook"
    assert task["pool"] == "paid:default"
    assert task["priority"] == 8

    assert get_in(task, ["payload", "execution"]) == %{
             "type" => "pipeline",
             "pipeline" => "resize-and-deliver"
           }

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "service_triggered",
               "ref" => %{
                 "service_id" => "webhook-listener",
                 "trigger_type" => "webhook",
                 "task_id" => "svc:webhook-listener:webhook:evt-123"
               }
             } ->
               true

             _ ->
               false
           end)

    pool = Mitos.WorkPool.fold_events(events)

    assert get_in(pool, ["tasks", task["id"], "status"]) == "queued"

    assert get_in(pool, ["tasks", task["id"], "tenant"]) == %{
             "org" => "org-a",
             "project" => "proj-a"
           }

    assert get_in(pool, ["tasks", task["id"], "external"]) == %{"ref" => "evt-123"}
  end

  test "internal execution is reserved for platform service units" do
    tenant_unit = %{
      id: "usage-meter",
      scope: "project",
      execution: %{type: "internal", internal: "meter_usage"}
    }

    assert {:error,
            {:invalid_service_unit, "internal execution is reserved for platform service units"}} =
             Mitos.ServiceRuntime.unit(tenant_unit)

    platform_unit =
      Mitos.ServiceRuntime.unit(
        %{
          id: "usage-meter",
          scope: "platform",
          execution: %{type: "internal", internal: "meter_usage"},
          schedule: %{type: "interval", every_ms: 180_000}
        },
        platform?: true
      )

    assert platform_unit["scope"] == "platform"
    assert platform_unit["execution"] == %{"type" => "internal", "internal" => "meter_usage"}
  end

  test "resolves targets presets pipelines and explains masked conflicts" do
    {:ok, view} =
      Mitos.ServiceRuntime.resolve_active_set(%{
        project_type: "marketplace-seller",
        active_targets: ["seller-endpoint.target"],
        active_pipelines: ["resize-and-deliver"],
        presets: [
          %{project_type: "marketplace-seller", enable: ["usage-meter", "local-poll"]}
        ],
        targets: [
          %{
            id: "seller-endpoint.target",
            requires: ["webhook-listener", "broker-warm"],
            wants: ["usage-meter"]
          }
        ],
        pipelines: [
          %{
            id: "resize-and-deliver",
            requires: %{services: ["render-pool"]}
          }
        ],
        units: [
          unit("webhook-listener",
            schedule: %{type: "webhook"},
            conflicts_with: ["local-poll"]
          ),
          unit("broker-warm", schedule: %{type: "continuous"}),
          unit("usage-meter", schedule: %{type: "interval", every_ms: 180_000}),
          unit("render-pool", schedule: %{type: "continuous"}, after: ["broker-warm"]),
          unit("local-poll", schedule: %{type: "interval", every_ms: 60_000})
        ]
      })

    assert Map.keys(view["active"]) |> Enum.sort() == [
             "broker-warm",
             "render-pool",
             "usage-meter",
             "webhook-listener"
           ]

    assert get_in(view, ["active", "webhook-listener", "why"]) == [
             "seller-endpoint.target (requires)"
           ]

    assert get_in(view, ["active", "usage-meter", "why"]) |> Enum.sort() == [
             "preset:marketplace-seller (enable)",
             "seller-endpoint.target (wants)"
           ]

    assert get_in(view, ["active", "render-pool", "why"]) == [
             "pipeline:resize-and-deliver (requires)"
           ]

    assert get_in(view, ["masked", "local-poll", "state"]) == "masked"

    assert get_in(view, ["masked", "local-poll", "masked_by"]) ==
             "conflicts_with webhook-listener"

    assert Enum.find_index(view["order"], &(&1 == "broker-warm")) <
             Enum.find_index(view["order"], &(&1 == "render-pool"))
  end

  test "unit relations pull dependencies fail missing hard requirements and order active set" do
    {:ok, view} =
      Mitos.ServiceRuntime.resolve_active_set(%{
        explicit_enable: ["api", "broken", "needs-disabled"],
        explicit_disable: ["disabled-dep"],
        units: [
          unit("api", requires: ["broker"], wants: ["metrics"], after: ["broker"]),
          unit("broker", requires: ["vault"], before: ["api"]),
          unit("vault", before: ["broker"]),
          unit("metrics", after: ["api"]),
          unit("broken", requires: ["absent"]),
          unit("needs-disabled", requires: ["disabled-dep"]),
          unit("disabled-dep", %{})
        ]
      })

    assert Map.keys(view["active"]) |> Enum.sort() == ["api", "broker", "metrics", "vault"]

    assert get_in(view, ["active", "broker", "why"]) == ["api (requires)"]
    assert get_in(view, ["active", "vault", "why"]) == ["broker (requires)"]
    assert get_in(view, ["active", "metrics", "why"]) == ["api (wants)"]

    assert get_in(view, ["failed", "broken", "failed_by"]) == "missing required absent"

    assert get_in(view, ["failed", "needs-disabled", "failed_by"]) ==
             "missing required disabled-dep"

    assert view["order"] == ["vault", "broker", "api", "metrics"]
  end

  test "hard conflicts fail at active-set resolution time" do
    config = %{
      explicit_enable: ["webhook-listener", "local-poll"],
      units: [
        unit("webhook-listener", conflicts_with: ["local-poll"]),
        unit("local-poll", conflicts_with: ["webhook-listener"])
      ]
    }

    assert {:error, {:activation_conflict, ["local-poll", "webhook-listener"]}} =
             Mitos.ServiceRuntime.resolve_active_set(config)
  end

  test "condition gates skip triggers until file env and command checks pass" do
    base_dir = unique_tmp_dir("service-runtime-conditions")
    on_exit(fn -> File.rm_rf!(base_dir) end)
    File.mkdir_p!(base_dir)

    ready_file = Path.join(base_dir, "ready.flag")
    trace = opened_trace(base_dir, "run-service-runtime-conditions")

    unit =
      unit("conditional-listener",
        schedule: %{type: "webhook"},
        conditions: [
          %{type: "file_exists", path: ready_file},
          %{type: "env_equals", name: "MODE", value: "ready"},
          %{type: "command", id: "cmd:readiness", command: "check-readiness"}
        ]
      )

    {trace, skipped_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-skipped"},
        caused_by: 0,
        env: %{"MODE" => "warming"},
        condition_results: %{"cmd:readiness" => true}
      )

    assert skipped_task == nil

    File.write!(ready_file, "ready")

    {trace, admitted_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-admitted"},
        caused_by: 0,
        env: %{"MODE" => "ready"},
        condition_results: %{"cmd:readiness" => true}
      )

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)
    pool = Mitos.WorkPool.fold_events(events)
    service_state = Mitos.ServiceRuntime.fold_events(events)

    assert admitted_task["id"] == "svc:conditional-listener:webhook:evt-admitted"
    assert get_in(pool, ["tasks", admitted_task["id"], "status"]) == "queued"
    assert get_in(pool, ["tasks", "svc:conditional-listener:webhook:evt-skipped"]) == nil

    assert get_in(service_state, ["services", "conditional-listener", "conditions_status"]) ==
             "passed"

    condition_events = Enum.filter(events, &(&1["type"] == "service_conditions_checked"))
    assert length(condition_events) == 2
    assert Enum.at(condition_events, 0)["payload"]["passed?"] == false
    assert Enum.at(condition_events, 1)["payload"]["passed?"] == true

    assert Enum.any?(events, fn event ->
             event["type"] == "service_trigger_skipped" and
               event["payload"]["reason"] == "conditions_failed"
           end)
  end

  test "file existence conditions do not follow symlinks" do
    base_dir = unique_tmp_dir("service-runtime-condition-symlink")
    outside = unique_tmp_dir("service-runtime-condition-symlink-outside")

    on_exit(fn ->
      File.rm_rf!(base_dir)
      File.rm_rf!(outside)
    end)

    File.mkdir_p!(base_dir)
    File.mkdir_p!(outside)

    outside_ready_file = Path.join(outside, "ready.flag")
    ready_link = Path.join(base_dir, "ready.flag")

    File.write!(outside_ready_file, "ready")
    File.ln_s!(outside_ready_file, ready_link)

    trace = opened_trace(base_dir, "run-service-runtime-condition-symlink")

    unit =
      unit("symlink-conditional-listener",
        schedule: %{type: "webhook"},
        conditions: [%{type: "file_exists", path: ready_link}]
      )

    {trace, skipped_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-symlink-skipped"},
        caused_by: 0
      )

    assert skipped_task == nil

    File.rm!(ready_link)
    File.write!(ready_link, "ready")

    {trace, admitted_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-regular-admitted"},
        caused_by: 0
      )

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)
    condition_events = Enum.filter(events, &(&1["type"] == "service_conditions_checked"))

    assert Enum.at(condition_events, 0)["payload"]["passed?"] == false
    assert Enum.at(condition_events, 1)["payload"]["passed?"] == true
    assert admitted_task["id"] == "svc:symlink-conditional-listener:webhook:evt-regular-admitted"

    pool = Mitos.WorkPool.fold_events(events)

    assert get_in(pool, ["tasks", "svc:symlink-conditional-listener:webhook:evt-symlink-skipped"]) ==
             nil

    assert get_in(pool, ["tasks", admitted_task["id"], "status"]) == "queued"
  end

  test "file existence conditions reject symlinked parent directories" do
    base_dir = unique_tmp_dir("service-runtime-condition-parent-symlink")
    outside = unique_tmp_dir("service-runtime-condition-parent-symlink-outside")

    on_exit(fn ->
      File.rm_rf!(base_dir)
      File.rm_rf!(outside)
    end)

    File.mkdir_p!(base_dir)
    File.mkdir_p!(outside)

    File.write!(Path.join(outside, "ready.flag"), "ready")
    ready_parent = Path.join(base_dir, "linked")
    ready_path = Path.join(ready_parent, "ready.flag")
    File.ln_s!(outside, ready_parent)

    trace = opened_trace(base_dir, "run-service-runtime-condition-parent-symlink")

    unit =
      unit("parent-symlink-conditional-listener",
        schedule: %{type: "webhook"},
        conditions: [%{type: "file_exists", path: ready_path}]
      )

    {trace, skipped_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-parent-symlink-skipped"},
        caused_by: 0
      )

    assert skipped_task == nil

    File.rm!(ready_parent)
    File.mkdir_p!(ready_parent)
    File.write!(ready_path, "ready")

    {_trace, admitted_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-regular-parent-admitted"},
        caused_by: 0
      )

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)
    condition_events = Enum.filter(events, &(&1["type"] == "service_conditions_checked"))

    assert Enum.at(condition_events, 0)["payload"]["passed?"] == false
    assert Enum.at(condition_events, 1)["payload"]["passed?"] == true

    assert admitted_task["id"] ==
             "svc:parent-symlink-conditional-listener:webhook:evt-regular-parent-admitted"
  end

  test "tick-driven interval schedules fire only when due" do
    base_dir = unique_tmp_dir("service-runtime-interval")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-service-runtime-interval")

    interval_unit =
      Mitos.ServiceRuntime.unit(unit("usage-meter", schedule: %{type: "interval", every_ms: 100}))

    {trace, [first_task]} =
      Mitos.ServiceRuntime.append_due_triggers(trace, [interval_unit],
        caused_by: 0,
        now_ms: 1_000
      )

    assert first_task["source"] == "service:usage-meter:interval"

    {trace, []} =
      Mitos.ServiceRuntime.append_due_triggers(trace, [interval_unit],
        caused_by: 0,
        now_ms: 1_050
      )

    {_trace, [second_task]} =
      Mitos.ServiceRuntime.append_due_triggers(trace, [interval_unit],
        caused_by: 0,
        now_ms: 1_100
      )

    assert second_task["id"] != first_task["id"]
    assert second_task["source"] == "service:usage-meter:interval"
  end

  test "continuous schedules do not duplicate active service tasks" do
    base_dir = unique_tmp_dir("service-runtime-continuous")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-service-runtime-continuous")

    continuous_unit =
      Mitos.ServiceRuntime.unit(unit("broker-warm", schedule: %{type: "continuous"}))

    {trace, [task]} =
      Mitos.ServiceRuntime.append_due_triggers(trace, [continuous_unit],
        caused_by: 0,
        now_ms: 2_000
      )

    assert task["source"] == "service:broker-warm:continuous"

    {_trace, []} =
      Mitos.ServiceRuntime.append_due_triggers(trace, [continuous_unit],
        caused_by: 0,
        now_ms: 3_000
      )
  end

  test "if_running skip records backpressure without enqueueing a task" do
    base_dir = unique_tmp_dir("service-runtime-skip")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-service-runtime-skip")

    unit =
      unit("webhook-listener",
        schedule: %{type: "webhook"},
        concurrency: %{max_instances: 1, if_running: "skip"}
      )

    {trace, first_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-1"},
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_status(trace, first_task["id"], :running,
        caused_by: 0,
        run_id: "worker-1"
      )

    {trace, skipped_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-2"},
        caused_by: 0
      )

    assert skipped_task == nil

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)
    pool = Mitos.WorkPool.fold_events(events)
    service_state = Mitos.ServiceRuntime.fold_events(events)

    assert get_in(pool, ["tasks", "svc:webhook-listener:webhook:evt-2"]) == nil
    assert get_in(service_state, ["services", "webhook-listener", "status"]) == "skipped"

    assert get_in(service_state, ["services", "webhook-listener", "last_skip_reason"]) ==
             "already_running"

    assert Enum.any?(events, &(&1["type"] == "service_trigger_skipped"))
  end

  test "if_running queue honors queue_max when the service is saturated" do
    base_dir = unique_tmp_dir("service-runtime-queue-max")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-service-runtime-queue-max")

    unit =
      unit("queued-listener",
        schedule: %{type: "webhook"},
        concurrency: %{max_instances: 1, if_running: "queue", queue_max: 1}
      )

    {trace, running_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-1"},
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_status(trace, running_task["id"], :running,
        caused_by: 0,
        run_id: "worker-1"
      )

    {trace, queued_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-2"},
        caused_by: 0
      )

    {trace, dropped_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-3"},
        caused_by: 0
      )

    assert queued_task["id"] == "svc:queued-listener:webhook:evt-2"
    assert dropped_task == nil

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)
    pool = Mitos.WorkPool.fold_events(events)
    service_state = Mitos.ServiceRuntime.fold_events(events)

    assert get_in(pool, ["tasks", queued_task["id"], "status"]) == "queued"
    assert get_in(pool, ["tasks", "svc:queued-listener:webhook:evt-3"]) == nil

    assert get_in(service_state, ["services", "queued-listener", "last_skip_reason"]) ==
             "queue_full"
  end

  test "if_running kill cancels active service tasks and enqueues the replacement" do
    base_dir = unique_tmp_dir("service-runtime-kill")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-service-runtime-kill")

    unit =
      unit("replace-listener",
        schedule: %{type: "webhook"},
        concurrency: %{max_instances: 1, if_running: "kill"}
      )

    {trace, first_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-1"},
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_status(trace, first_task["id"], :running,
        caused_by: 0,
        run_id: "worker-1"
      )

    {trace, replacement_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-2"},
        caused_by: 0
      )

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)
    pool = Mitos.WorkPool.fold_events(events)
    service_state = Mitos.ServiceRuntime.fold_events(events)

    assert replacement_task["id"] == "svc:replace-listener:webhook:evt-2"
    assert get_in(pool, ["tasks", first_task["id"], "status"]) == "cancelled"
    assert get_in(pool, ["tasks", replacement_task["id"], "status"]) == "queued"
    assert get_in(service_state, ["services", "replace-listener", "kill_count"]) == 1

    assert Enum.any?(events, &(&1["type"] == "service_instances_killed"))
  end

  test "unhealthy health check can kill active service tasks" do
    base_dir = unique_tmp_dir("service-runtime-health-kill")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-service-runtime-health-kill")

    unit =
      unit("health-listener",
        schedule: %{type: "webhook"},
        health: %{type: "heartbeat", on_unhealthy: "kill"}
      )

    {trace, task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-health"},
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_status(trace, task["id"], :running,
        caused_by: 0,
        run_id: "worker-health"
      )

    {trace, check} =
      Mitos.ServiceRuntime.append_health_result(
        trace,
        unit,
        %{type: "heartbeat", status: "unhealthy", message: "missed heartbeat"},
        caused_by: 0,
        now_ms: 5_000
      )

    assert check["status"] == "unhealthy"

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)
    pool = Mitos.WorkPool.fold_events(events)
    service_state = Mitos.ServiceRuntime.fold_events(events)

    assert get_in(pool, ["tasks", task["id"], "status"]) == "cancelled"
    assert get_in(service_state, ["services", "health-listener", "health_status"]) == "unhealthy"

    assert get_in(service_state, ["services", "health-listener", "last_unhealthy_action"]) ==
             "kill"

    assert Enum.any?(events, &(&1["type"] == "service_health_checked"))
    assert Enum.any?(events, &(&1["type"] == "service_unhealthy_action"))
  end

  test "unhealthy restart action cancels active tasks and schedules a restart" do
    base_dir = unique_tmp_dir("service-runtime-health-restart")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-service-runtime-health-restart")

    unit =
      Mitos.ServiceRuntime.unit(
        unit("restart-health-listener",
          schedule: %{type: "webhook"},
          health: %{type: "heartbeat", on_unhealthy: "restart"},
          restart_policy: %{action: "retry", max_retries: 2, backoff_ms: 50}
        )
      )

    {trace, task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-health-restart"},
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_status(trace, task["id"], :running,
        caused_by: 0,
        run_id: "worker-health-restart"
      )

    {trace, _check} =
      Mitos.ServiceRuntime.append_health_result(
        trace,
        unit,
        %{type: "heartbeat", status: "unhealthy", message: "restart me"},
        caused_by: 0,
        now_ms: 6_000
      )

    {trace, []} =
      Mitos.ServiceRuntime.append_due_restarts(trace, [unit],
        caused_by: 0,
        now_ms: 6_049
      )

    {trace, [restart_task]} =
      Mitos.ServiceRuntime.append_due_restarts(trace, [unit],
        caused_by: 0,
        now_ms: 6_050
      )

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)
    pool = Mitos.WorkPool.fold_events(events)
    service_state = Mitos.ServiceRuntime.fold_events(events)

    assert get_in(pool, ["tasks", task["id"], "status"]) == "cancelled"
    assert restart_task["source"] == "service:restart-health-listener:event"

    assert get_in(service_state, ["services", "restart-health-listener", "restart_count"]) ==
             1

    assert Enum.any?(events, &(&1["type"] == "service_restart_scheduled"))
    assert Enum.any?(events, &(&1["type"] == "service_restart_fired"))
  end

  test "circuit breaker blocks triggers until cooldown half-opens it" do
    base_dir = unique_tmp_dir("service-runtime-circuit")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-service-runtime-circuit")

    unit =
      unit("circuit-listener",
        schedule: %{type: "webhook"},
        health: %{type: "heartbeat", on_unhealthy: "log"},
        circuit_breaker: %{threshold: 2, cooldown_ms: 100}
      )

    {trace, _check} =
      Mitos.ServiceRuntime.append_health_result(
        trace,
        unit,
        %{status: "unhealthy", message: "first miss"},
        caused_by: 0,
        now_ms: 1_000
      )

    {trace, _check} =
      Mitos.ServiceRuntime.append_health_result(
        trace,
        unit,
        %{status: "unhealthy", message: "second miss"},
        caused_by: 0,
        now_ms: 1_010
      )

    {trace, blocked_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-blocked"},
        caused_by: 0,
        now_ms: 1_050
      )

    assert blocked_task == nil

    {trace, half_open_task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit,
        %{type: "webhook", delivery_id: "evt-half-open"},
        caused_by: 0,
        now_ms: 1_120
      )

    assert half_open_task["id"] == "svc:circuit-listener:webhook:evt-half-open"

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)
    service_state = Mitos.ServiceRuntime.fold_events(events)
    pool = Mitos.WorkPool.fold_events(events)

    assert get_in(service_state, ["services", "circuit-listener", "circuit_state"]) ==
             "half-open"

    assert get_in(service_state, ["services", "circuit-listener", "health_failure_streak"]) == 2
    assert get_in(pool, ["tasks", "svc:circuit-listener:webhook:evt-blocked"]) == nil
    assert get_in(pool, ["tasks", half_open_task["id"], "status"]) == "queued"

    assert Enum.any?(events, &(&1["type"] == "service_circuit_open"))
    assert Enum.any?(events, &(&1["type"] == "service_circuit_half_open"))
  end

  test "event schedules fire for matching effect events" do
    base_dir = unique_tmp_dir("service-runtime-event")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-service-runtime-event")

    event_unit =
      Mitos.ServiceRuntime.unit(
        unit("after-capability",
          schedule: %{type: "event", event_type: "capability_finished"}
        )
      )

    event = %{
      "kind" => "effect",
      "type" => "capability_finished",
      "run_id" => "child-run",
      "seq" => 42,
      "payload" => %{"status" => "completed"}
    }

    {_trace, [task]} =
      Mitos.ServiceRuntime.append_event_triggers(trace, [event_unit], event,
        caused_by: 0,
        now_ms: 3_000
      )

    assert task["source"] == "service:after-capability:event"
    assert get_in(task, ["payload", "inputs", "event", "type"]) == "capability_finished"
    assert get_in(task, ["payload", "trigger", "idempotency_key"]) =~ "after-capability:event"

    {_trace, []} =
      Mitos.ServiceRuntime.append_event_triggers(
        trace,
        [event_unit],
        %{"kind" => "effect", "type" => "budget_charged", "run_id" => "child-run", "seq" => 43},
        caused_by: 0,
        now_ms: 3_001
      )
  end

  test "service task completion emits chainable service outcome events" do
    base_dir = unique_tmp_dir("service-runtime-outcome")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-service-runtime-outcome")

    {trace, _task} =
      Mitos.ServiceRuntime.append_trigger(
        trace,
        unit("primary-service", schedule: %{type: "manual"}),
        %{type: "manual", idempotency_key: "primary-1"},
        caused_by: 0
      )

    {:ok, _task, result, trace} =
      Mitos.WorkPool.drain_once(
        trace,
        "services",
        fn task -> {:ok, %{"run_id" => "child-run", "task_id" => task["id"]}} end,
        caused_by: 0
      )

    assert result["run_id"] == "child-run"

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)

    success_event =
      Enum.find(events, &(&1["type"] == "service.succeeded:primary-service"))

    assert success_event["ref"]["service_id"] == "primary-service"
    assert Enum.any?(events, &(&1["type"] == "service.finished:primary-service"))

    service_state = Mitos.ServiceRuntime.fold_events(events)
    assert get_in(service_state, ["services", "primary-service", "status"]) == "succeeded"
    assert get_in(service_state, ["services", "primary-service", "success_count"]) == 1
    assert get_in(service_state, ["services", "primary-service", "finished_count"]) == 1

    chain_unit =
      Mitos.ServiceRuntime.unit(
        unit("after-primary",
          schedule: %{type: "event", event_type: "service.succeeded:primary-service"}
        )
      )

    {_trace, [chain_task]} =
      Mitos.ServiceRuntime.append_event_triggers(trace, [chain_unit], success_event,
        caused_by: 0,
        now_ms: 4_000
      )

    assert chain_task["source"] == "service:after-primary:event"

    assert get_in(chain_task, ["payload", "inputs", "event", "type"]) ==
             "service.succeeded:primary-service"
  end

  test "restart policy schedules retry with durable backoff and fires when due" do
    base_dir = unique_tmp_dir("service-runtime-retry")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-service-runtime-retry")

    unit =
      Mitos.ServiceRuntime.unit(
        unit("retry-listener",
          restart_policy: %{action: "retry", max_retries: 2, backoff_ms: 100}
        )
      )

    failed_event = failed_service_event("retry-listener", "task-failed", 42)

    {trace, [schedule]} =
      Mitos.ServiceRuntime.append_restart_triggers(trace, [unit], failed_event,
        caused_by: 0,
        now_ms: 1_000
      )

    assert schedule["attempt"] == 1
    assert schedule["due_at_ms"] == 1_100

    {trace, []} =
      Mitos.ServiceRuntime.append_due_restarts(trace, [unit],
        caused_by: 0,
        now_ms: 1_099
      )

    {trace, [task]} =
      Mitos.ServiceRuntime.append_due_restarts(trace, [unit],
        caused_by: 0,
        now_ms: 1_100
      )

    assert task["source"] == "service:retry-listener:event"
    assert get_in(task, ["payload", "trigger", "event_type"]) == "service.restart:retry-listener"
    assert get_in(task, ["payload", "inputs", "restart", "failed_task_id"]) == "task-failed"

    {trace, []} =
      Mitos.ServiceRuntime.append_due_restarts(trace, [unit],
        caused_by: 0,
        now_ms: 1_500
      )

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)
    service_state = Mitos.ServiceRuntime.fold_events(events)

    assert get_in(service_state, ["services", "retry-listener", "restart_count"]) == 1

    assert get_in(service_state, ["services", "retry-listener", "last_restart_fired_id"]) ==
             schedule["id"]

    assert get_in(
             service_state,
             ["services", "retry-listener", "restart_schedules", schedule["id"], "status"]
           ) == "fired"
  end

  test "restart policy skip and abort record durable decisions without retry tasks" do
    base_dir = unique_tmp_dir("service-runtime-restart-terminal")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-service-runtime-restart-terminal")
    skip_unit = Mitos.ServiceRuntime.unit(unit("skip-listener", restart_policy: "skip"))
    abort_unit = Mitos.ServiceRuntime.unit(unit("abort-listener", restart_policy: "abort"))

    {trace, [skip_decision]} =
      Mitos.ServiceRuntime.append_restart_triggers(
        trace,
        [skip_unit],
        failed_service_event("skip-listener", "skip-task", 50),
        caused_by: 0,
        now_ms: 2_000
      )

    {trace, [abort_decision]} =
      Mitos.ServiceRuntime.append_restart_triggers(
        trace,
        [abort_unit],
        failed_service_event("abort-listener", "abort-task", 51),
        caused_by: 0,
        now_ms: 2_000
      )

    {_trace, []} =
      Mitos.ServiceRuntime.append_due_restarts(trace, [skip_unit, abort_unit],
        caused_by: 0,
        now_ms: 3_000
      )

    assert skip_decision["action"] == "skip"
    assert abort_decision["action"] == "abort"

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)
    service_state = Mitos.ServiceRuntime.fold_events(events)

    assert get_in(service_state, ["services", "skip-listener", "last_restart_skip_reason"]) ==
             "restart_policy_skip"

    assert get_in(service_state, ["services", "abort-listener", "status"]) == "aborted"
  end

  defp opened_trace(base_dir, run_id) do
    trace =
      Mitos.Trace.open!(
        run_id: run_id,
        base_dir: base_dir,
        launch_spec: %{"provider" => "service-runtime", "task" => "trigger"}
      )

    Mitos.Trace.append_decision(trace, %{
      actor: "orchestrator",
      type: "advance",
      target: "service-runtime",
      params: %{},
      inputs_hash: "sha256:service-runtime",
      rationale: "fire service trigger",
      budget: %{"tokens" => 0, "usd" => 0, "wall_ms" => 0, "remaining" => %{}}
    })
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end

  defp unit(id, attrs) do
    attrs = Map.new(attrs)

    Map.merge(
      %{
        id: id,
        scope: "project",
        execution: %{type: "pipeline", pipeline: "#{id}-pipeline"},
        schedule: %{type: "manual"}
      },
      attrs
    )
  end

  defp failed_service_event(service_id, task_id, seq) do
    %{
      "kind" => "effect",
      "type" => "service.failed:#{service_id}",
      "run_id" => "child-run",
      "seq" => seq,
      "ref" => %{"service_id" => service_id, "task_id" => task_id},
      "payload" => %{"service_id" => service_id, "task_id" => task_id, "run_id" => "child-run"}
    }
  end
end
