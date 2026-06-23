defmodule Mitos.PipelineRunnerTest do
  use ExUnit.Case, async: false

  import Plug.Test

  setup do
    base_dir = unique_tmp_dir("pipeline-runner-traces")
    workloads_dir = unique_tmp_dir("pipeline-runner-workloads")
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

  test "pipeline execute route runs concrete workflow steps under parent pipeline", %{
    workloads_dir: workloads_dir
  } do
    write_workload!(workloads_dir, "planner", %{
      provider: "fake",
      template: "Plan {{objective}}",
      title: "Planner",
      capability: %{name: "planner", harnesses: ["fake"], trust_tier: "T1"}
    })

    write_workload!(workloads_dir, "verifier", %{
      provider: "fake",
      template: "Verify {{objective}} after {{previous_results}}",
      title: "Verifier",
      capability: %{name: "verifier", harnesses: ["fake"], trust_tier: "T1"}
    })

    create_pipeline!(%{
      id: "release-flow",
      title: "Release flow",
      provider: "fake",
      template: "Run release flow for {{objective}}",
      workflow: %{
        goal: "Release",
        stages: ["plan", "verify"],
        steps: [
          %{id: "plan", name: "Plan release", capability_id: "planner"},
          %{id: "verify", name: "Verify release", capability_id: "verifier"}
        ]
      }
    })

    conn =
      json_conn(:post, "/pipelines/release-flow/execute", %{
        inputs: %{objective: "ship rc"},
        task: %{task_id: "release-42"},
        tenant: %{org: "buyer-a", project: "default", actor: "operator"}
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 200
    body = Jason.decode!(conn.resp_body)

    assert body["pipeline_id"] == "release-flow"
    assert body["status"] == "completed"
    assert ["plan", "verify"] = Enum.map(body["steps"], & &1["id"])
    assert Enum.all?(body["steps"], &is_binary(&1["run_id"]))
    assert [plan, verify] = body["steps"]
    assert plan["summary"] =~ "FAKE_FINAL: Plan ship rc"
    assert verify["summary"] =~ "FAKE_FINAL: Verify ship rc after"
    assert verify["summary"] =~ "Plan release: FAKE_FINAL: Plan ship rc"

    board = conn(:get, "/work-pool") |> Mitos.Web.Router.call([]) |> decode_body()
    tasks = board["tasks"]
    assert Enum.map(tasks, & &1["id"]) == ["release-42:plan", "release-42:verify"]
    assert Enum.all?(tasks, &(&1["pipeline"] == "workload:release-flow"))
    assert Enum.all?(tasks, &(&1["status"] == "done"))
    assert Enum.map(tasks, &get_in(&1, ["payload", "workload_id"])) == ["planner", "verifier"]

    auto_conn =
      json_conn(:post, "/pipelines/release-flow/execute", %{
        inputs: %{objective: "auto id"}
      })
      |> Mitos.Web.Router.call([])

    assert auto_conn.status == 200
    auto_body = Jason.decode!(auto_conn.resp_body)
    execution_id = auto_body["execution_id"]
    auto_board = conn(:get, "/work-pool") |> Mitos.Web.Router.call([]) |> decode_body()

    assert auto_board["tasks"]
           |> Enum.filter(&String.starts_with?(&1["id"], execution_id <> ":"))
           |> Enum.map(& &1["id"]) == ["#{execution_id}:plan", "#{execution_id}:verify"]
  end

  test "pipeline execute route rejects pipelines without steps" do
    create_pipeline!(%{
      id: "single",
      provider: "fake",
      template: "{{objective}}",
      workflow: %{goal: "single"}
    })

    conn =
      json_conn(:post, "/pipelines/single/execute", %{inputs: %{objective: "x"}})
      |> Mitos.Web.Router.call([])

    assert conn.status == 422
    assert Jason.decode!(conn.resp_body)["error"] == "invalid pipeline definition"
  end

  defp create_pipeline!(body) do
    conn = json_conn(:post, "/pipelines", body) |> Mitos.Web.Router.call([])
    assert conn.status == 201
    Jason.decode!(conn.resp_body)["pipeline"]
  end

  defp write_workload!(dir, id, json) do
    File.write!(Path.join(dir, id <> ".json"), Jason.encode!(json))
  end

  defp json_conn(method, path, body) do
    method
    |> conn(path, Jason.encode!(body))
    |> Plug.Conn.put_req_header("content-type", "application/json")
  end

  defp decode_body(conn), do: Jason.decode!(conn.resp_body)

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end

  defp restore_env(key, nil), do: Application.delete_env(:mitos, key)
  defp restore_env(key, value), do: Application.put_env(:mitos, key, value)
end
