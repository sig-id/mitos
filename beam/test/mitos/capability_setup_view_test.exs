defmodule Mitos.CapabilitySetupViewTest do
  use ExUnit.Case, async: false

  import Plug.Test

  setup do
    base_dir = unique_tmp_dir("capability-setup-traces")
    workloads_dir = unique_tmp_dir("capability-setup-workloads")
    File.mkdir_p!(base_dir)
    File.mkdir_p!(workloads_dir)

    previous_trace_base_dir = Application.get_env(:mitos, :trace_base_dir)
    previous_workloads_dir = Application.get_env(:mitos, :workloads_dir)
    previous_sandbox_providers = Application.get_env(:mitos, :sandbox_providers)
    previous_ready_secret = System.get_env("MITOS_SETUP_READY_SECRET")
    previous_missing_secret = System.get_env("MITOS_SETUP_MISSING_SECRET")

    Application.put_env(:mitos, :trace_base_dir, base_dir)
    Application.put_env(:mitos, :workloads_dir, workloads_dir)
    Application.put_env(:mitos, :sandbox_providers, [Mitos.SandboxProvider.LocalSidecar])
    System.put_env("MITOS_SETUP_READY_SECRET", "host-secret-value")
    System.delete_env("MITOS_SETUP_MISSING_SECRET")

    on_exit(fn ->
      restore_app_env(:trace_base_dir, previous_trace_base_dir)
      restore_app_env(:workloads_dir, previous_workloads_dir)
      restore_app_env(:sandbox_providers, previous_sandbox_providers)
      restore_system_env("MITOS_SETUP_READY_SECRET", previous_ready_secret)
      restore_system_env("MITOS_SETUP_MISSING_SECRET", previous_missing_secret)
      File.rm_rf!(base_dir)
      File.rm_rf!(workloads_dir)
    end)

    {:ok, base_dir: base_dir, workloads_dir: workloads_dir}
  end

  test "setup view exposes runtime readiness without exposing secret values", %{
    workloads_dir: workloads_dir
  } do
    write_workload!(workloads_dir, "ready", %{
      provider: "fake",
      template: "Do {{objective}}",
      title: "Ready wrapper",
      env: %{"INLINE_SECRET" => "stored-secret-value"},
      env_passthrough: ["MITOS_SETUP_READY_SECRET"],
      capability: %{
        name: "ready",
        harnesses: ["fake"],
        trust_tier: "T1",
        skill: "skill:ready",
        result: %{"schema" => "result.v1"}
      }
    })

    view = Mitos.CapabilitySetupView.all()

    assert view["totals"] == %{
             "capabilities" => 1,
             "needs_setup" => 0,
             "runnable" => 1,
             "with_runs" => 0
           }

    assert [capability] = view["capabilities"]
    assert capability["id"] == "ready"
    assert capability["template"] == "Do {{objective}}"
    assert capability["runtime"]["provider"] == "fake"
    assert capability["runtime"]["transport_order"] == ["stdio-json", "pty"]
    assert capability["capability"]["harnesses"] == ["fake"]
    assert capability["setup"]["can_run"] == true
    assert capability["setup"]["status"] == "ready"
    assert capability["env"]["stored_keys"] == ["INLINE_SECRET"]

    assert capability["env"]["passthrough"] == [
             %{
               "name" => "MITOS_SETUP_READY_SECRET",
               "present" => true,
               "source" => "host_env"
             }
           ]

    assert Enum.any?(capability["files"], &(&1["kind"] == "workload_definition"))
    assert Enum.any?(capability["files"], &(&1["kind"] == "skill"))

    encoded = Jason.encode!(view)
    refute encoded =~ "stored-secret-value"
    refute encoded =~ "host-secret-value"
  end

  test "setup view marks missing host env keys as needs_env", %{workloads_dir: workloads_dir} do
    write_workload!(workloads_dir, "needs-env", %{
      provider: "fake",
      template: "{{objective}}",
      env_passthrough: ["MITOS_SETUP_MISSING_SECRET"]
    })

    {:ok, capability} = Mitos.CapabilitySetupView.get("needs-env")

    assert capability["setup"]["can_run"] == false
    assert capability["setup"]["status"] == "needs_env"
    assert capability["env"]["missing_passthrough"] == ["MITOS_SETUP_MISSING_SECRET"]

    assert [%{"kind" => "env", "keys" => ["MITOS_SETUP_MISSING_SECRET"]}] =
             capability["setup"]["blocking"]
  end

  test "GET /capabilities/setup returns the operator setup projection", %{
    base_dir: base_dir,
    workloads_dir: workloads_dir
  } do
    write_workload!(workloads_dir, "echo", %{
      provider: "fake",
      template: "{{objective}}",
      title: "Echo"
    })

    trace = opened_trace(base_dir, "run-wrapper-history")

    trace =
      Mitos.WorkPool.append_enqueue(
        trace,
        %{
          id: "capability-smoke",
          pool: "interactive",
          pipeline: "workload:echo",
          source: "manual",
          tenant: %{org: "buyer-a", project: "default"},
          payload: %{
            workload_id: "echo",
            intent: "Echo test",
            inputs: %{objective: "test wrapper"}
          }
        },
        caused_by: 0
      )

    _trace =
      Mitos.WorkPool.append_status(trace, "capability-smoke", :done,
        caused_by: 0,
        run_id: "wl-history"
      )

    conn = conn(:get, "/capabilities/setup") |> Mitos.Web.Router.call([])
    assert conn.status == 200

    body = Jason.decode!(conn.resp_body)
    assert body["execute_endpoint"] == "/workloads/:id/execute"
    assert body["ui_setup"]["definition_route"] == "#/capabilities/new"
    assert body["totals"]["with_runs"] == 1

    assert [
             %{
               "id" => "echo",
               "setup" => %{"can_run" => true},
               "work" => %{
                 "pipeline_id" => "workload:echo",
                 "tasks" => %{"total" => 1, "done" => 1, "active" => 0, "failed" => 0},
                 "recent_runs" => ["wl-history"],
                 "recent_tasks" => [
                   %{
                     "id" => "capability-smoke",
                     "status" => "done",
                     "pool" => "interactive",
                     "runs" => ["wl-history"],
                     "input_keys" => ["objective"]
                   }
                 ]
               }
             }
           ] = body["capabilities"]
  end

  test "capability setup routes create editable workload-backed wrappers", %{
    workloads_dir: workloads_dir
  } do
    body = %{
      id: "ui-wrapper",
      title: "UI wrapper",
      description: "Created entirely from the capability setup UI",
      provider: "fake",
      template: "Handle {{objective}} for {{expected_output}}",
      cwd: ".",
      timeout_ms: 45_000,
      approvals: "deny",
      transport_order: ["stdio-json"],
      env_passthrough: ["MITOS_SETUP_MISSING_SECRET"],
      capability: %{
        name: "ui-wrapper",
        harnesses: ["fake"],
        trust_tier: "T1",
        skill: "skill:ui-wrapper",
        result: %{"schema" => "result.v1"}
      }
    }

    conn = json_conn(:post, "/capabilities/setup", body) |> Mitos.Web.Router.call([])
    assert conn.status == 201

    created = Jason.decode!(conn.resp_body)["capability"]
    assert created["id"] == "ui-wrapper"
    assert created["template"] == "Handle {{objective}} for {{expected_output}}"
    assert created["runtime"]["provider"] == "fake"
    assert created["runtime"]["cwd"] == "."
    assert created["runtime"]["transport_order"] == ["stdio-json"]
    assert created["setup"]["status"] == "needs_env"
    assert created["env"]["missing_passthrough"] == ["MITOS_SETUP_MISSING_SECRET"]
    assert created["capability"]["harnesses"] == ["fake"]

    persisted =
      workloads_dir
      |> Path.join("ui-wrapper.json")
      |> File.read!()
      |> Jason.decode!()

    assert persisted["env_passthrough"] == ["MITOS_SETUP_MISSING_SECRET"]
    assert persisted["capability"]["skill"] == "skill:ui-wrapper"
    assert persisted["capability"]["result"] == %{"schema" => "result.v1"}
  end

  test "capability setup update preserves stored env values without exposing them", %{
    workloads_dir: workloads_dir
  } do
    write_workload!(workloads_dir, "stored-env", %{
      provider: "fake",
      template: "{{objective}}",
      title: "Stored env",
      env: %{"INLINE_SECRET" => "stored-secret-value"},
      env_passthrough: ["MITOS_SETUP_READY_SECRET"]
    })

    conn =
      json_conn(:put, "/capabilities/setup/stored-env", %{
        title: "Updated stored env",
        description: "Updated from UI",
        provider: "fake",
        template: "Updated {{objective}}",
        timeout_ms: 60_000,
        approvals: "deny",
        transport_order: ["stdio-json"],
        env_passthrough: ["MITOS_SETUP_READY_SECRET"],
        capability: %{name: "stored-env", harnesses: ["fake"], trust_tier: "T1"}
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 200
    body = Jason.decode!(conn.resp_body)
    updated = body["capability"]

    assert updated["title"] == "Updated stored env"
    assert updated["env"]["stored_keys"] == ["INLINE_SECRET"]
    assert updated["setup"]["status"] == "ready"

    persisted =
      workloads_dir
      |> Path.join("stored-env.json")
      |> File.read!()
      |> Jason.decode!()

    assert persisted["env"] == %{"INLINE_SECRET" => "stored-secret-value"}

    encoded = Jason.encode!(body)
    refute encoded =~ "stored-secret-value"
    refute encoded =~ "host-secret-value"
  end

  test "capability setup routes reject unsafe ids", %{workloads_dir: workloads_dir} do
    conn =
      json_conn(:post, "/capabilities/setup", %{
        id: "../escape",
        provider: "fake",
        template: "{{objective}}"
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 422
    assert Jason.decode!(conn.resp_body)["error"] == "invalid capability setup"
    refute File.exists?(Path.join(workloads_dir, "../escape.json"))
  end

  defp write_workload!(dir, id, json) do
    File.write!(Path.join(dir, id <> ".json"), Jason.encode!(json))
  end

  defp opened_trace(base_dir, run_id) do
    trace =
      Mitos.Trace.open!(
        run_id: run_id,
        base_dir: base_dir,
        launch_spec: %{"provider" => "work-pool", "task" => "capability-history"}
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

  defp json_conn(method, path, body) do
    method
    |> conn(path, Jason.encode!(body))
    |> Plug.Conn.put_req_header("content-type", "application/json")
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end

  defp restore_app_env(key, nil), do: Application.delete_env(:mitos, key)
  defp restore_app_env(key, value), do: Application.put_env(:mitos, key, value)

  defp restore_system_env(key, nil), do: System.delete_env(key)
  defp restore_system_env(key, value), do: System.put_env(key, value)
end
