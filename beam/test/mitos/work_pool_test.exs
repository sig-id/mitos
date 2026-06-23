defmodule Mitos.WorkPoolTest do
  use ExUnit.Case, async: true

  test "folds task lifecycle effects and selects ready tasks by discipline" do
    base_dir = unique_tmp_dir("work-pool")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "task-a",
          tenant: %{org: "org-a", project: "proj-a"},
          pool: "batch",
          priority: 1,
          payload: %{intent: "prepare input"}
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "task-b",
          tenant: %{org: "org-a", project: "proj-a"},
          pool: "batch",
          priority: 10,
          deps: [%{from: "task-a", relation: "FS", strength: "hard"}],
          payload: %{intent: "use prepared input"}
        },
        caused_by: 0
      )

    state = work_pool_state("run-work-pool", base_dir)

    assert state["ready_tasks"] == ["task-a"]
    assert state["blocked_tasks"] == ["task-b"]

    assert {:ok, %{"id" => "task-a"}} =
             Mitos.WorkPool.select_task(state, %{id: "batch", discipline: :priority})

    _trace =
      Mitos.WorkPool.append_status(trace, "task-a", :done,
        caused_by: 0,
        result: %{signal: "ship"}
      )

    state = work_pool_state("run-work-pool", base_dir)

    assert state["ready_tasks"] == ["task-b"]
    assert state["blocked_tasks"] == []
    assert get_in(state, ["tasks", "task-a", "status"]) == "done"
    assert get_in(state, ["tasks", "task-a", "result"]) == %{"signal" => "ship"}
    assert {:ok, %{"id" => "task-b"}} = Mitos.WorkPool.select_task(state, "batch")
  end

  test "supports fifo lifo priority deadline and deterministic random disciplines" do
    state =
      [
        task_effect("old-low", "batch", 1, 0, %{"deadline_at" => "2026-06-23T10:00:00Z"}),
        task_effect("new-high", "batch", 10, 1, %{"deadline_at" => "2026-06-23T12:00:00Z"}),
        task_effect("urgent", "batch", 2, 2, %{"deadline_at" => "2026-06-23T09:00:00Z"})
      ]
      |> Mitos.WorkPool.fold_events()

    assert {:ok, %{"id" => "old-low"}} =
             Mitos.WorkPool.select_task(state, %{id: "batch", discipline: :fifo})

    assert {:ok, %{"id" => "urgent"}} =
             Mitos.WorkPool.select_task(state, %{id: "batch", discipline: :lifo})

    assert {:ok, %{"id" => "new-high"}} =
             Mitos.WorkPool.select_task(state, %{id: "batch", discipline: :priority})

    assert {:ok, %{"id" => "urgent"}} =
             Mitos.WorkPool.select_task(state, %{id: "batch", discipline: :deadline_edf})

    assert {:ok, random_pick} =
             Mitos.WorkPool.select_task(state, %{id: "batch", discipline: :random})

    assert {:ok, ^random_pick} =
             Mitos.WorkPool.select_task(state, %{id: "batch", discipline: :random})
  end

  test "weighted_fair prefers tenants with less running work" do
    base_dir = unique_tmp_dir("work-pool-weighted-fair")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-weighted-fair")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{id: "a-running", pool: "shared", tenant: %{org: "tenant-a"}},
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{id: "a-next", pool: "shared", tenant: %{org: "tenant-a"}},
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{id: "b-next", pool: "shared", tenant: %{org: "tenant-b"}},
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_status(trace, "a-running", :running,
        caused_by: 0,
        run_id: "worker-a"
      )

    state = work_pool_state(trace.run_id, base_dir)

    assert {:ok, %{"id" => "b-next"}} =
             Mitos.WorkPool.select_task(state, %{
               id: "shared",
               discipline: :weighted_fair,
               fairness_key: :tenant
             })
  end

  test "pool concurrency caps active scheduled and running tasks per tenant" do
    base_dir = unique_tmp_dir("work-pool-concurrency")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-concurrency")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "running",
          pool: "limited",
          tenant: %{org: "tenant-a"},
          payload: %{intent: "active"}
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "next",
          pool: "limited",
          tenant: %{org: "tenant-a"},
          payload: %{intent: "next"}
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "other-tenant-next",
          pool: "limited",
          tenant: %{org: "tenant-b"},
          payload: %{intent: "other"}
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_status(trace, "running", :running,
        caused_by: 0,
        run_id: "worker-running"
      )

    state = work_pool_state(trace.run_id, base_dir)

    assert {:ok, %{"id" => "other-tenant-next"}} =
             Mitos.WorkPool.select_task(state, %{id: "limited", concurrency: 1})

    assert {:ok, %{"id" => "next"}} =
             Mitos.WorkPool.select_task(state, %{id: "limited", concurrency: 2})
  end

  test "pool scope filters candidates by tenant boundary" do
    base_dir = unique_tmp_dir("work-pool-scope")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-scope")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "org-a-proj-a",
          pool: "scoped",
          tenant: %{org: "org-a", project: "proj-a", run_id: "run-a"},
          payload: %{intent: "a"}
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "org-a-proj-b",
          pool: "scoped",
          tenant: %{org: "org-a", project: "proj-b", run_id: "run-b"},
          payload: %{intent: "b"}
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "org-b-proj-a",
          pool: "scoped",
          tenant: %{org: "org-b", project: "proj-a", run_id: "run-c"},
          payload: %{intent: "c"}
        },
        caused_by: 0
      )

    state = work_pool_state(trace.run_id, base_dir)

    assert {:ok, %{"id" => "org-a-proj-b"}} =
             Mitos.WorkPool.select_task(state, %{
               id: "scoped",
               scope: "project",
               tenant: %{org: "org-a", project: "proj-b"}
             })

    assert {:ok, %{"id" => "org-a-proj-a"}} =
             Mitos.WorkPool.select_task(state, %{
               id: "scoped",
               scope: "org",
               tenant: %{org: "org-a"}
             })

    assert {:ok, %{"id" => "org-a-proj-a"}} =
             Mitos.WorkPool.select_task(state, %{id: "scoped", scope: "platform"})
  end

  test "conflict constraints do not block tasks from another tenant" do
    base_dir = unique_tmp_dir("work-pool-conflict-tenant")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-conflict-tenant")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{id: "a-running", pool: "shared", tenant: %{org: "tenant-a"}},
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "a-conflict",
          pool: "shared",
          tenant: %{org: "tenant-a"},
          deps: [%{from: "a-running", relation: "conflict"}]
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "b-conflict",
          pool: "shared",
          tenant: %{org: "tenant-b"},
          deps: [%{from: "a-running", relation: "conflict"}]
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_status(trace, "a-running", :running,
        caused_by: 0,
        run_id: "worker-a"
      )

    state = work_pool_state(trace.run_id, base_dir)

    assert {:ok, %{"id" => "b-conflict"}} = Mitos.WorkPool.select_task(state, "shared")
  end

  test "conflict constraints declared by running tasks block same-tenant candidates" do
    base_dir = unique_tmp_dir("work-pool-conflict-symmetric")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-conflict-symmetric")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "running",
          pool: "shared",
          tenant: %{org: "tenant-a"},
          deps: [%{from: "candidate", relation: "mutex"}]
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{id: "candidate", pool: "shared", tenant: %{org: "tenant-a"}},
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_status(trace, "running", :running,
        caused_by: 0,
        run_id: "worker-a"
      )

    state = work_pool_state(trace.run_id, base_dir)

    assert :empty = Mitos.WorkPool.select_task(state, "shared")
  end

  test "folds invoke request as a parked parent and queued calls-pool child" do
    base_dir = unique_tmp_dir("work-pool-invoke")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-invoke")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{id: "parent", pool: "interactive", payload: %{intent: "ask another capability"}},
        caused_by: 0
      )

    {trace, child_task, admission} =
      Mitos.WorkPool.append_invoke(
        trace,
        "parent",
        %{id: "child", payload: %{intent: "answer parent"}},
        caused_by: 0,
        return_to: %{socket: "authority", request_id: "invoke-1"}
      )

    assert child_task["id"] == "child"
    assert admission["status"] == "accepted"

    state = work_pool_state("run-work-pool-invoke", base_dir)

    assert get_in(state, ["tasks", "parent", "status"]) == "parked"
    assert get_in(state, ["tasks", "parent", "parked_reason"]) == "awaiting-invoke"
    assert get_in(state, ["tasks", "child", "pool"]) == "calls"
    assert get_in(state, ["tasks", "child", "source"]) == "invoke:parent"

    assert {:ok, %{"id" => "child"}} =
             Mitos.WorkPool.select_task(state, %{id: "calls", discipline: :lifo})

    _trace =
      Mitos.WorkPool.append_subtask_return(
        trace,
        "parent",
        "child",
        %{signal: "ship", summary: "subtask complete"},
        caused_by: 0
      )

    state = work_pool_state("run-work-pool-invoke", base_dir)

    assert get_in(state, ["tasks", "parent", "status"]) == "queued"

    assert get_in(state, ["tasks", "parent", "last_subtask_result"]) == %{
             "signal" => "ship",
             "summary" => "subtask complete"
           }

    assert get_in(state, ["invocations", "parent->child", "status"]) == "returned"
  end

  test "expired invoke timeout resumes parent with an error result" do
    base_dir = unique_tmp_dir("work-pool-invoke-timeout")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-invoke-timeout")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{id: "parent", pool: "interactive", payload: %{intent: "ask another capability"}},
        caused_by: 0
      )

    {trace, child_task, admission} =
      Mitos.WorkPool.append_invoke(
        trace,
        "parent",
        %{id: "child", payload: %{intent: "answer parent"}},
        caused_by: 0,
        return_to: %{socket: "authority", request_id: "invoke-timeout"},
        timeout_ms: 1_000
      )

    assert child_task["id"] == "child"
    assert admission["status"] == "accepted"

    state = work_pool_state("run-work-pool-invoke-timeout", base_dir)

    assert get_in(state, ["invocations", "parent->child", "timeout_ms"]) == 1_000
    assert get_in(state, ["tasks", "parent", "status"]) == "parked"
    assert Mitos.WorkPool.expired_invokes(state, "1970-01-01T00:00:00.000Z") == []

    assert [%{"parent_task" => "parent", "child_task" => "child"}] =
             Mitos.WorkPool.expired_invokes(state, "9999-01-01T00:00:00.000Z")

    {trace, timed_out} =
      Mitos.WorkPool.append_expired_timeouts(trace,
        caused_by: 0,
        now: "9999-01-01T00:00:00.000Z"
      )

    assert Enum.map(timed_out, & &1["child_task"]) == ["child"]

    state = work_pool_state("run-work-pool-invoke-timeout", base_dir)

    result = %{"ok" => false, "error" => "timeout", "timeout_ms" => 1_000}

    assert get_in(state, ["tasks", "parent", "status"]) == "queued"
    assert get_in(state, ["tasks", "parent", "last_subtask_result"]) == result
    assert get_in(state, ["tasks", "child", "status"]) == "failed"
    assert get_in(state, ["tasks", "child", "status_reason"]) == "timeout"
    assert get_in(state, ["tasks", "child", "result"]) == result
    assert get_in(state, ["invocations", "parent->child", "status"]) == "timed_out"
    assert get_in(state, ["invocations", "parent->child", "result"]) == result
    assert Mitos.WorkPool.expired_invokes(state, "9999-01-01T00:00:00.000Z") == []

    _trace =
      Mitos.WorkPool.append_subtask_return(
        trace,
        "parent",
        "child",
        %{signal: "late", summary: "arrived after timeout"},
        caused_by: 0
      )

    state = work_pool_state("run-work-pool-invoke-timeout", base_dir)

    assert get_in(state, ["tasks", "parent", "last_subtask_result"]) == result
    assert get_in(state, ["invocations", "parent->child", "status"]) == "timed_out"
  end

  test "invoke admission enforces depth allowlist budget and dedupe" do
    base_dir = unique_tmp_dir("work-pool-invoke-governance")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-invoke-governance")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "parent",
          pool: "interactive",
          tenant: %{org: "org-a", project: "proj-a"},
          provenance: %{depth: 1},
          payload: %{intent: "ask another capability"}
        },
        caused_by: 0
      )

    {trace, accepted, accepted_admission} =
      Mitos.WorkPool.append_invoke(
        trace,
        "parent",
        %{
          id: "child-ok",
          pipeline: "workload:allowed-cap",
          budget: %{tokens: 60},
          payload: %{intent: "allowed invoke"}
        },
        caused_by: 0,
        allowed_capabilities: ["allowed-cap"],
        budget_cap: %{tokens: 100},
        dedupe_key: "invoke:allowed-cap"
      )

    {trace, duplicate, duplicate_admission} =
      Mitos.WorkPool.append_invoke(
        trace,
        "parent",
        %{
          id: "child-duplicate",
          pipeline: "workload:allowed-cap",
          payload: %{intent: "duplicate invoke"}
        },
        caused_by: 0,
        allowed_capabilities: ["allowed-cap"],
        dedupe_key: "invoke:allowed-cap"
      )

    {trace, too_deep, too_deep_admission} =
      Mitos.WorkPool.append_invoke(
        trace,
        "parent",
        %{id: "child-too-deep", pipeline: "workload:allowed-cap"},
        caused_by: 0,
        allowed_capabilities: ["allowed-cap"],
        max_depth: 1
      )

    {trace, not_allowed, not_allowed_admission} =
      Mitos.WorkPool.append_invoke(
        trace,
        "parent",
        %{id: "child-denied", pipeline: "workload:denied-cap"},
        caused_by: 0,
        allowed_capabilities: ["allowed-cap"]
      )

    {_trace, over_budget, over_budget_admission} =
      Mitos.WorkPool.append_invoke(
        trace,
        "parent",
        %{
          id: "child-over-budget",
          pipeline: "workload:allowed-cap",
          budget: %{tokens: 50}
        },
        caused_by: 0,
        allowed_capabilities: ["allowed-cap"],
        budget_cap: %{tokens: 100}
      )

    assert accepted["id"] == "child-ok"
    assert accepted_admission["status"] == "accepted"
    assert duplicate == nil
    assert duplicate_admission["status"] == "deduplicated"
    assert too_deep == nil
    assert too_deep_admission["reason"] == "depth_exceeded"
    assert not_allowed == nil
    assert not_allowed_admission["reason"] == "not_allowed"
    assert over_budget == nil
    assert over_budget_admission["reason"] == "budget_exceeded"
    assert over_budget_admission["budget_usage"]["tokens"] == 110.0

    state = work_pool_state("run-work-pool-invoke-governance", base_dir)
    assert get_in(state, ["tasks", "child-ok", "status"]) == "queued"
    assert get_in(state, ["tasks", "child-ok", "provenance", "depth"]) == 2

    assert get_in(state, ["tasks", "child-ok", "tenant"]) == %{
             "org" => "org-a",
             "project" => "proj-a"
           }

    assert get_in(state, ["tasks", "child-duplicate"]) == nil
    assert get_in(state, ["tasks", "child-too-deep"]) == nil
    assert get_in(state, ["tasks", "child-denied"]) == nil
    assert get_in(state, ["tasks", "child-over-budget"]) == nil
    assert get_in(state, ["invocations", "parent->child-duplicate", "status"]) == "deduplicated"
    assert get_in(state, ["invocations", "parent->child-too-deep", "status"]) == "rejected"
    assert get_in(state, ["invocations", "parent->child-denied", "reason"]) == "not_allowed"

    assert get_in(state, ["invocations", "parent->child-over-budget", "reason"]) ==
             "budget_exceeded"
  end

  test "agent-emitted follow-up tasks are governed enqueued and deduplicated" do
    base_dir = unique_tmp_dir("work-pool-emitted")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-emitted")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "parent",
          pool: "interactive",
          tenant: %{org: "org-a", project: "proj-a"},
          provenance: %{depth: 1},
          payload: %{intent: "discover follow-up"}
        },
        caused_by: 0
      )

    {trace, emitted} =
      Mitos.WorkPool.append_emitted_task(
        trace,
        "parent",
        %{
          id: "follow-up",
          payload: %{intent: "check generated artifact", dedupe_key: "artifact-check"},
          budget: %{tokens: 100, usd: 0.25, wall_ms: 1_000}
        },
        caused_by: 0,
        max_depth: 3,
        budget_cap: %{tokens: 200, usd: 1.0, wall_ms: 2_000}
      )

    {trace, duplicate} =
      Mitos.WorkPool.append_emitted_task(
        trace,
        "parent",
        %{
          id: "follow-up-duplicate",
          payload: %{intent: "check generated artifact", dedupe_key: "artifact-check"}
        },
        caused_by: 0,
        max_depth: 3
      )

    assert emitted["id"] == "follow-up"
    assert duplicate == nil

    state = work_pool_state(trace.run_id, base_dir)

    assert get_in(state, ["tasks", "follow-up", "status"]) == "queued"
    assert get_in(state, ["tasks", "follow-up", "source"]) == "emitted:parent"

    assert get_in(state, ["tasks", "follow-up", "tenant"]) == %{
             "org" => "org-a",
             "project" => "proj-a"
           }

    assert get_in(state, ["tasks", "follow-up", "provenance", "depth"]) == 2
    assert get_in(state, ["tasks", "follow-up-duplicate"]) == nil

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)
    emitted_events = Enum.filter(events, &(&1["type"] == "task_emitted"))

    assert Enum.map(emitted_events, &get_in(&1, ["payload", "admission", "status"])) == [
             "accepted",
             "deduplicated"
           ]
  end

  test "agent-emitted task admission enforces depth and budget caps" do
    base_dir = unique_tmp_dir("work-pool-emitted-guardrails")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-emitted-guardrails")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "parent",
          pool: "interactive",
          provenance: %{depth: 2},
          payload: %{intent: "discover follow-up"}
        },
        caused_by: 0
      )

    {trace, depth_rejected} =
      Mitos.WorkPool.append_emitted_task(
        trace,
        "parent",
        %{id: "too-deep", payload: %{intent: "too deep"}},
        caused_by: 0,
        max_depth: 2
      )

    {trace, budget_rejected} =
      Mitos.WorkPool.append_emitted_task(
        trace,
        "parent",
        %{id: "too-expensive", budget: %{tokens: 500}, payload: %{intent: "too expensive"}},
        caused_by: 0,
        max_depth: 3,
        budget_cap: %{tokens: 100}
      )

    assert depth_rejected == nil
    assert budget_rejected == nil

    state = work_pool_state(trace.run_id, base_dir)
    assert get_in(state, ["tasks", "too-deep"]) == nil
    assert get_in(state, ["tasks", "too-expensive"]) == nil

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)

    assert Enum.map(Enum.filter(events, &(&1["type"] == "task_emitted")), fn event ->
             get_in(event, ["payload", "admission", "reason"])
           end) == ["depth_exceeded", "budget_exceeded"]
  end

  test "agent-emitted budget cap is cumulative per tenant" do
    base_dir = unique_tmp_dir("work-pool-emitted-tenant-budget")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-emitted-tenant-budget")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "parent-a",
          pool: "interactive",
          tenant: %{org: "org-a", project: "proj-a"},
          payload: %{intent: "tenant a"}
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "parent-b",
          pool: "interactive",
          tenant: %{org: "org-b", project: "proj-b"},
          payload: %{intent: "tenant b"}
        },
        caused_by: 0
      )

    {trace, first} =
      Mitos.WorkPool.append_emitted_task(
        trace,
        "parent-a",
        %{id: "tenant-a-first", budget: %{tokens: 60}, payload: %{intent: "first"}},
        caused_by: 0,
        budget_cap: %{tokens: 100}
      )

    {trace, rejected} =
      Mitos.WorkPool.append_emitted_task(
        trace,
        "parent-a",
        %{id: "tenant-a-second", budget: %{tokens: 50}, payload: %{intent: "second"}},
        caused_by: 0,
        budget_cap: %{tokens: 100}
      )

    {trace, other_tenant} =
      Mitos.WorkPool.append_emitted_task(
        trace,
        "parent-b",
        %{id: "tenant-b-first", budget: %{tokens: 50}, payload: %{intent: "other tenant"}},
        caused_by: 0,
        budget_cap: %{tokens: 100}
      )

    assert first["id"] == "tenant-a-first"
    assert rejected == nil
    assert other_tenant["id"] == "tenant-b-first"

    state = work_pool_state(trace.run_id, base_dir)
    assert get_in(state, ["tasks", "tenant-a-first", "status"]) == "queued"
    assert get_in(state, ["tasks", "tenant-a-second"]) == nil
    assert get_in(state, ["tasks", "tenant-b-first", "status"]) == "queued"

    events = Mitos.Trace.read_events(trace.run_id, base_dir: base_dir)

    rejected_event =
      Enum.find(events, fn event ->
        event["type"] == "task_emitted" and get_in(event, ["ref", "task_id"]) == "tenant-a-second"
      end)

    assert get_in(rejected_event, ["payload", "admission", "reason"]) == "budget_exceeded"
    assert get_in(rejected_event, ["payload", "admission", "budget_usage", "tokens"]) == 110.0
  end

  test "detects hard enabling dependency cycles before selection" do
    state =
      [
        task_effect("task-a", "batch", 0, 0, %{})
        |> put_in(["payload", "task", "deps"], [%{"from" => "task-b"}]),
        task_effect("task-b", "batch", 0, 1, %{})
        |> put_in(["payload", "task", "deps"], [%{"from" => "task-a"}])
      ]
      |> Mitos.WorkPool.fold_events()

    assert [cycle] = state["cycles"]
    assert hd(cycle) == List.last(cycle)
    assert MapSet.new(cycle) == MapSet.new(["task-a", "task-b"])

    assert {:error, {:dependency_cycle, ^cycle}} = Mitos.WorkPool.validate_acyclic(state)
    assert {:error, {:dependency_cycle, ^cycle}} = Mitos.WorkPool.select_task(state, "batch")
  end

  test "ignores soft and conflict edges for dependency cycle validation" do
    state =
      [
        task_effect("task-a", "batch", 0, 0, %{})
        |> put_in(["payload", "task", "deps"], [
          %{"from" => "task-b", "strength" => "soft"}
        ]),
        task_effect("task-b", "batch", 0, 1, %{})
        |> put_in(["payload", "task", "deps"], [
          %{"from" => "task-a", "relation" => "conflict"}
        ])
      ]
      |> Mitos.WorkPool.fold_events()

    assert state["cycles"] == []
    assert :ok = Mitos.WorkPool.validate_acyclic(state)
    assert {:ok, %{"id" => "task-a"}} = Mitos.WorkPool.select_task(state, "batch")
  end

  test "conditional data dependencies gate readiness and inject upstream result" do
    base_dir = unique_tmp_dir("work-pool-conditional-data")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-conditional-data")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{id: "producer", pool: "batch", payload: %{intent: "produce"}},
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "ship-branch",
          pool: "batch",
          deps: [
            %{
              from: "producer",
              relation: "FS",
              strength: "hard",
              payload: "data",
              as: "producer_output",
              guard: %{signal: "ship"}
            }
          ],
          payload: %{intent: "ship"}
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "reject-branch",
          pool: "batch",
          deps: [
            %{
              from: "producer",
              relation: "FS",
              strength: "hard",
              payload: "data",
              guard: %{signal: "reject"}
            }
          ],
          payload: %{intent: "reject"}
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_status(trace, "producer", :done,
        caused_by: 0,
        result: %{signal: "ship", artifact: "artifact:result.json"}
      )

    state = work_pool_state(trace.run_id, base_dir)

    assert state["ready_tasks"] == ["ship-branch"]
    assert state["blocked_tasks"] == ["reject-branch"]

    assert get_in(state, [
             "tasks",
             "ship-branch",
             "payload",
             "dependency_results",
             "producer_output"
           ]) == %{"signal" => "ship", "artifact" => "artifact:result.json"}

    assert get_in(state, ["tasks", "reject-branch", "payload", "dependency_results"]) == nil
  end

  test "any join readies after the first matching hard dependency" do
    base_dir = unique_tmp_dir("work-pool-any-join")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-any-join")

    trace =
      Mitos.WorkPool.append_enqueue(trace, %{id: "producer-a", pool: "batch"}, caused_by: 0)

    trace =
      Mitos.WorkPool.append_enqueue(trace, %{id: "producer-b", pool: "batch"}, caused_by: 0)

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "joiner",
          pool: "batch",
          join: "any",
          deps: [
            %{from: "producer-a", relation: "FS", payload: "data"},
            %{from: "producer-b", relation: "FS", payload: "data"}
          ]
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_status(trace, "producer-a", :done,
        caused_by: 0,
        result: %{signal: "ship", artifact: "artifact:a.json"}
      )

    trace =
      Mitos.WorkPool.append_status(trace, "producer-b", :running,
        caused_by: 0,
        run_id: "producer-b-run"
      )

    state = work_pool_state(trace.run_id, base_dir)

    assert "joiner" in state["ready_tasks"]
    refute "joiner" in state["blocked_tasks"]

    assert get_in(state, ["tasks", "joiner", "payload", "dependency_results"]) == %{
             "producer-a" => %{"signal" => "ship", "artifact" => "artifact:a.json"}
           }
  end

  test "k_of_n join waits for its quorum" do
    base_dir = unique_tmp_dir("work-pool-k-of-n-join")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-k-of-n-join")

    trace =
      Enum.reduce(["producer-a", "producer-b", "producer-c"], trace, fn id, trace ->
        Mitos.WorkPool.append_enqueue(trace, %{id: id, pool: "batch"}, caused_by: 0)
      end)

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "joiner",
          pool: "batch",
          join: %{rule: "k_of_n", k: 2},
          deps: [
            %{from: "producer-a", relation: "FS", payload: "data"},
            %{from: "producer-b", relation: "FS", payload: "data"},
            %{from: "producer-c", relation: "FS", payload: "data"}
          ]
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_status(trace, "producer-a", :done,
        caused_by: 0,
        result: %{artifact: "artifact:a.json"}
      )

    trace =
      Mitos.WorkPool.append_status(trace, "producer-b", :running,
        caused_by: 0,
        run_id: "producer-b-run"
      )

    trace =
      Mitos.WorkPool.append_status(trace, "producer-c", :running,
        caused_by: 0,
        run_id: "producer-c-run"
      )

    state = work_pool_state(trace.run_id, base_dir)
    refute "joiner" in state["ready_tasks"]
    assert "joiner" in state["blocked_tasks"]

    trace =
      Mitos.WorkPool.append_status(trace, "producer-b", :done,
        caused_by: 0,
        result: %{artifact: "artifact:b.json"}
      )

    state = work_pool_state(trace.run_id, base_dir)
    assert "joiner" in state["ready_tasks"]

    assert get_in(state, ["tasks", "joiner", "payload", "dependency_results"]) == %{
             "producer-a" => %{"artifact" => "artifact:a.json"},
             "producer-b" => %{"artifact" => "artifact:b.json"}
           }
  end

  test "drains one selected task through dispatch and records lifecycle effects" do
    base_dir = unique_tmp_dir("work-pool-drain")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-drain")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{id: "task-low", pool: "batch", priority: 1, payload: %{intent: "low"}},
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{id: "task-high", pool: "batch", priority: 10, payload: %{intent: "high"}},
        caused_by: 0
      )

    {:ok, task, result, trace} =
      Mitos.WorkPool.drain_once(
        trace,
        %{id: "batch", discipline: :priority},
        fn task -> {:ok, %{summary: "dispatched #{task["id"]}"}} end,
        caused_by: 0
      )

    assert task["id"] == "task-high"
    assert result == %{"summary" => "dispatched task-high"}

    state = work_pool_state(trace.run_id, base_dir)

    assert get_in(state, ["tasks", "task-high", "status"]) == "done"
    assert get_in(state, ["tasks", "task-high", "runs"]) == ["run-work-pool-drain"]
    assert get_in(state, ["tasks", "task-high", "result"]) == result
    assert get_in(state, ["tasks", "task-low", "status"]) == "queued"
  end

  test "drain records dispatch failures and refuses cyclic graphs" do
    base_dir = unique_tmp_dir("work-pool-drain-fail")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-work-pool-drain-fail")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{id: "task-fail", pool: "batch", payload: %{intent: "fail"}},
        caused_by: 0
      )

    {:error, task, :boom, trace} =
      Mitos.WorkPool.drain_once(trace, "batch", fn _task -> {:error, :boom} end, caused_by: 0)

    assert task["id"] == "task-fail"
    state = work_pool_state(trace.run_id, base_dir)
    assert get_in(state, ["tasks", "task-fail", "status"]) == "failed"
    assert get_in(state, ["tasks", "task-fail", "status_reason"]) == ":boom"

    cyclic_trace = opened_trace(base_dir, "run-work-pool-drain-cycle")

    cyclic_trace =
      Mitos.WorkPool.append_enqueue(
        cyclic_trace,
        %{id: "task-a", pool: "batch", deps: [%{from: "task-b"}]},
        caused_by: 0
      )

    cyclic_trace =
      Mitos.WorkPool.append_enqueue(
        cyclic_trace,
        %{id: "task-b", pool: "batch", deps: [%{from: "task-a"}]},
        caused_by: 0
      )

    {:error, {:dependency_cycle, cycle}, ^cyclic_trace} =
      Mitos.WorkPool.drain_once(
        cyclic_trace,
        "batch",
        fn task -> flunk("cyclic graph dispatched #{task["id"]}") end,
        caused_by: 0
      )

    assert MapSet.new(cycle) == MapSet.new(["task-a", "task-b"])
  end

  defp opened_trace(base_dir, run_id) do
    trace =
      Mitos.Trace.open!(
        run_id: run_id,
        base_dir: base_dir,
        launch_spec: %{"provider" => "work-pool", "task" => "drain"}
      )

    Mitos.Trace.append_decision(trace, %{
      actor: "orchestrator",
      type: "advance",
      target: "work-pool",
      params: %{},
      inputs_hash: "sha256:state",
      rationale: "mutate work-pool state",
      budget: %{"tokens" => 0, "usd" => 0, "wall_ms" => 0, "remaining" => %{}}
    })
  end

  defp work_pool_state(run_id, base_dir) do
    run_id
    |> Mitos.Trace.read_events(base_dir: base_dir)
    |> Mitos.WorkPool.fold_events()
  end

  defp task_effect(id, pool, priority, seq, payload) do
    %{
      "kind" => "effect",
      "type" => "task_enqueued",
      "seq" => seq,
      "ts" => "2026-06-22T12:00:0#{seq}Z",
      "caused_by" => 0,
      "ref" => %{"task_id" => id, "pool" => pool},
      "payload" => %{
        "task" => %{
          "id" => id,
          "pool" => pool,
          "priority" => priority,
          "payload" => payload
        }
      }
    }
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end
end
