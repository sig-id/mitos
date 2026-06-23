defmodule Mitos.WorkPoolViewTest do
  use ExUnit.Case, async: false

  import Plug.Test

  setup do
    base_dir = unique_tmp_dir("work-pool-view-traces")
    workloads_dir = unique_tmp_dir("work-pool-view-workloads")
    File.mkdir_p!(base_dir)
    File.mkdir_p!(workloads_dir)

    previous_trace_base_dir = Application.get_env(:mitos, :trace_base_dir)
    previous_workloads_dir = Application.get_env(:mitos, :workloads_dir)
    Application.put_env(:mitos, :trace_base_dir, base_dir)
    Application.put_env(:mitos, :workloads_dir, workloads_dir)

    on_exit(fn ->
      restore_env(:trace_base_dir, previous_trace_base_dir)
      restore_env(:workloads_dir, previous_workloads_dir)
      File.rm_rf!(base_dir)
      File.rm_rf!(workloads_dir)
    end)

    {:ok, base_dir: base_dir, workloads_dir: workloads_dir}
  end

  test "work-pool route returns board-ready task columns", %{base_dir: base_dir} do
    trace = opened_trace(base_dir, "run-board")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "prepare",
          pool: "interactive",
          pipeline: "workload:demo",
          tenant: %{org: "buyer-a", project: "default"},
          payload: %{workload_id: "demo", intent: "prepare"}
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "review",
          pool: "interactive",
          pipeline: "workload:demo",
          deps: [%{from: "prepare", relation: "FS", strength: "hard"}],
          payload: %{workload_id: "demo", intent: "review"}
        },
        caused_by: 0
      )

    _trace =
      Mitos.WorkPool.append_status(trace, "prepare", :running,
        caused_by: 0,
        run_id: "run-board"
      )

    conn = conn(:get, "/work-pool") |> Mitos.Web.Router.call([])
    assert conn.status == 200
    body = Jason.decode!(conn.resp_body)

    assert body["totals"]["all"] == 2
    assert body["totals"]["running"] == 1
    assert body["totals"]["blocked"] == 1
    assert [%{"id" => "prepare", "status" => "running"}] = body["columns"]["running"]
    assert [%{"id" => "review", "status" => "blocked"}] = body["columns"]["blocked"]
    assert [%{"id" => "interactive", "tasks" => 2, "active" => 2}] = body["pools"]
  end

  test "pipelines route returns workload registry with task counts", %{
    base_dir: base_dir,
    workloads_dir: workloads_dir
  } do
    File.write!(
      Path.join(workloads_dir, "demo.json"),
      Jason.encode!(%{
        provider: "fake",
        template: "Do {{objective}}",
        title: "Demo pipeline",
        description: "A packaged workload",
        workflow: %{
          goal: "Ship a demo artifact",
          stages: ["plan", "work", "verify"],
          success_criteria: ["artifact is ready"]
        },
        capability: %{name: "demo", harnesses: ["fake"], trust_tier: "T1"}
      })
    )

    trace = opened_trace(base_dir, "run-pipeline")

    _trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "task-demo",
          pool: "interactive",
          pipeline: "workload:demo",
          payload: %{workload_id: "demo", intent: "Do ship"}
        },
        caused_by: 0
      )

    conn = conn(:get, "/pipelines") |> Mitos.Web.Router.call([])
    assert conn.status == 200
    body = Jason.decode!(conn.resp_body)

    assert %{"totals" => %{"pipelines" => 1, "active" => 1}} = body

    assert [
             %{
               "id" => "demo",
               "pipeline_id" => "workload:demo",
               "title" => "Demo pipeline",
               "placeholders" => ["objective"],
               "workflow" => %{
                 "goal" => "Ship a demo artifact",
                 "stages" => ["plan", "work", "verify"],
                 "success_criteria" => ["artifact is ready"]
               },
               "tasks" => %{"total" => 1, "active" => 1}
             }
           ] = body["pipelines"]

    refute Map.has_key?(hd(body["pipelines"]), "provider")
  end

  test "pipeline route groups structured step tasks into execution history", %{
    base_dir: base_dir,
    workloads_dir: workloads_dir
  } do
    File.write!(
      Path.join(workloads_dir, "release-flow.json"),
      Jason.encode!(%{
        provider: "fake",
        template: "Do {{objective}}",
        title: "Release flow",
        workflow: %{
          steps: [
            %{id: "plan", name: "Plan release", capability_id: "planner"},
            %{id: "verify", name: "Verify release", capability_id: "verifier"}
          ]
        },
        capability: %{name: "release-flow", harnesses: ["fake"], trust_tier: "T1"}
      })
    )

    trace = opened_trace(base_dir, "run-pipeline-execution")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "pipe-abc:plan",
          pool: "interactive",
          pipeline: "workload:release-flow",
          payload: %{workload_id: "planner", intent: "Plan ship rc"}
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "pipe-abc:verify",
          pool: "interactive",
          pipeline: "workload:release-flow",
          payload: %{workload_id: "verifier", intent: "Verify ship rc"}
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "legacy-release-task",
          pool: "interactive",
          pipeline: "workload:release-flow",
          payload: %{workload_id: "release-flow", intent: "Legacy run"}
        },
        caused_by: 0
      )

    trace =
      Mitos.WorkPool.append_status(trace, "pipe-abc:plan", :done,
        caused_by: 0,
        run_id: "run-plan"
      )

    _trace =
      Mitos.WorkPool.append_status(trace, "pipe-abc:verify", :running,
        caused_by: 0,
        run_id: "run-verify"
      )

    conn = conn(:get, "/pipelines/release-flow") |> Mitos.Web.Router.call([])
    assert conn.status == 200
    pipeline = Jason.decode!(conn.resp_body)["pipeline"]

    execution = Enum.find(pipeline["executions"], &(&1["id"] == "pipe-abc"))
    assert execution["status"] == "running"
    assert execution["counts"] == %{"total" => 2, "active" => 1, "done" => 1, "failed" => 0}
    assert execution["runs"] == ["run-plan", "run-verify"]

    assert [
             %{
               "id" => "plan",
               "name" => "Plan release",
               "capability_id" => "planner",
               "task_id" => "pipe-abc:plan",
               "status" => "done",
               "runs" => ["run-plan"]
             },
             %{
               "id" => "verify",
               "name" => "Verify release",
               "capability_id" => "verifier",
               "task_id" => "pipe-abc:verify",
               "status" => "running",
               "runs" => ["run-verify"]
             }
           ] = execution["steps"]

    legacy_execution = Enum.find(pipeline["executions"], &(&1["id"] == "legacy-release-task"))
    assert legacy_execution["status"] == "queued"

    assert [%{"task_id" => "legacy-release-task", "capability_id" => "release-flow"}] =
             legacy_execution["steps"]

    assert pipeline["recent_runs"] == ["run-plan", "run-verify"]
  end

  test "pipeline authoring routes create fetch and update workload-backed definitions", %{
    workloads_dir: workloads_dir
  } do
    create_body = %{
      id: "new-pipeline",
      title: "New pipeline",
      description: "Created from the UI",
      provider: "fake",
      template: "Do {{objective}}",
      timeout_ms: 45_000,
      trust_tier: "T1",
      workflow: %{
        goal: "Prepare a completed buyer deliverable",
        stages: ["plan", "work", "verify"],
        allowed_capabilities: ["demo"],
        steps: [
          %{
            id: "review",
            name: "Review request",
            capability_id: "demo",
            notes: "first pass"
          }
        ],
        constraints: ["stay inside buyer scope"],
        success_criteria: ["proof is available"],
        checkpoints: ["after:verify"],
        budget: %{tokens: 10_000, usd: 1.5, wall_ms: 45_000}
      }
    }

    create_conn = json_conn(:post, "/pipelines", create_body) |> Mitos.Web.Router.call([])
    assert create_conn.status == 201
    created = Jason.decode!(create_conn.resp_body)["pipeline"]

    assert created["id"] == "new-pipeline"
    assert created["pipeline_id"] == "workload:new-pipeline"
    assert created["provider"] == "fake"
    assert created["template"] == "Do {{objective}}"
    assert created["workflow"]["goal"] == "Prepare a completed buyer deliverable"
    assert created["workflow"]["stages"] == ["plan", "work", "verify"]

    assert created["workflow"]["steps"] == [
             %{
               "id" => "review",
               "name" => "Review request",
               "capability_id" => "demo",
               "notes" => "first pass"
             }
           ]

    assert created["workflow"]["budget"] == %{
             "tokens" => 10_000,
             "usd" => 1.5,
             "wall_ms" => 45_000
           }

    assert created["capability"]["trust_tier"] == "T1"
    assert File.regular?(Path.join(workloads_dir, "new-pipeline.json"))

    get_conn = conn(:get, "/pipelines/new-pipeline") |> Mitos.Web.Router.call([])
    assert get_conn.status == 200
    fetched = Jason.decode!(get_conn.resp_body)["pipeline"]
    assert fetched["title"] == "New pipeline"
    assert fetched["editable"] == true

    duplicate_conn = json_conn(:post, "/pipelines", create_body) |> Mitos.Web.Router.call([])
    assert duplicate_conn.status == 409

    update_conn =
      json_conn(:put, "/pipelines/new-pipeline", %{
        title: "Updated pipeline",
        description: "Updated from the UI",
        provider: "fake",
        template: "Review {{objective}} for {{expected_output}}",
        timeout_ms: 90_000,
        trust_tier: "T1",
        workflow: %{
          goal: "Review an objective against the expected output",
          stages: ["review", "report"],
          allowed_capabilities: ["demo", "reviewer"],
          steps: [
            %{id: "critique", name: "Critique", capability_id: "reviewer"}
          ],
          success_criteria: ["review names risks"]
        }
      })
      |> Mitos.Web.Router.call([])

    assert update_conn.status == 200
    updated = Jason.decode!(update_conn.resp_body)["pipeline"]
    assert updated["title"] == "Updated pipeline"
    assert updated["placeholders"] == ["objective", "expected_output"]
    assert updated["timeout_ms"] == 90_000
    assert updated["workflow"]["goal"] == "Review an objective against the expected output"
    assert updated["workflow"]["stages"] == ["review", "report"]
    assert updated["workflow"]["allowed_capabilities"] == ["demo", "reviewer"]

    assert updated["workflow"]["steps"] == [
             %{"id" => "critique", "name" => "Critique", "capability_id" => "reviewer"}
           ]
  end

  test "pipeline authoring rejects unsafe ids", %{workloads_dir: workloads_dir} do
    conn =
      json_conn(:post, "/pipelines", %{
        id: "../escape",
        provider: "fake",
        template: "Do {{objective}}"
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 422
    assert Jason.decode!(conn.resp_body)["error"] == "invalid pipeline definition"
    refute File.exists?(Path.join(workloads_dir, "../escape.json"))
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

  defp restore_env(key, nil), do: Application.delete_env(:mitos, key)
  defp restore_env(key, value), do: Application.put_env(:mitos, key, value)

  defp json_conn(method, path, body) do
    method
    |> conn(path, Jason.encode!(body))
    |> Plug.Conn.put_req_header("content-type", "application/json")
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end
end
