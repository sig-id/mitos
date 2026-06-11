defmodule Mitos.RunsTest do
  use ExUnit.Case, async: false

  import Plug.Test

  alias Mitos.Runs

  setup do
    tmp = Path.join(System.tmp_dir!(), "mitos-runs-#{System.unique_integer([:positive])}")
    File.mkdir_p!(tmp)
    previous = Application.get_env(:mitos, :trace_base_dir)
    Application.put_env(:mitos, :trace_base_dir, tmp)

    on_exit(fn ->
      if previous do
        Application.put_env(:mitos, :trace_base_dir, previous)
      else
        Application.delete_env(:mitos, :trace_base_dir)
      end

      File.rm_rf!(tmp)
    end)

    {:ok, base_dir: tmp}
  end

  defp write_run!(base_dir, run_id, opts \\ []) do
    dir = Path.join([base_dir, ".runs", run_id])
    File.mkdir_p!(dir)

    manifest = [
      "schema_version: 1",
      "run_id: #{run_id}",
      "created_at_ms: #{Keyword.get(opts, :created_at_ms, 1_780_000_000_000)}",
      "provider: #{Keyword.get(opts, :provider, "fake")}",
      "task: #{Keyword.get(opts, :task, "hello")}",
      ""
    ]

    File.write!(Path.join(dir, "run.yaml"), Enum.join(manifest, "\n"))

    events =
      opts
      |> Keyword.get(:events, [])
      |> Enum.with_index()
      |> Enum.map(fn {event, index} ->
        Jason.encode!(%{
          "schema_version" => 1,
          "run_id" => run_id,
          "sequence" => index,
          "timestamp_ms" => 1_780_000_000_000 + index,
          "event" => event
        })
      end)

    File.write!(Path.join(dir, "events.jsonl"), Enum.map_join(events, "", &(&1 <> "\n")))

    approvals =
      opts
      |> Keyword.get(:approvals, [])
      |> Enum.map_join("", &(Jason.encode!(&1) <> "\n"))

    File.write!(Path.join(dir, "approvals.jsonl"), approvals)
    dir
  end

  describe "list/0" do
    test "returns [] when the runs directory does not exist" do
      assert Runs.list() == []
    end

    test "summarizes runs newest first with derived status", %{base_dir: base_dir} do
      write_run!(base_dir, "run-old",
        created_at_ms: 100,
        events: [
          %{"type" => "RUN_STARTED", "provider" => "fake"},
          %{"type" => "PROCESS_EXITED", "code" => 0},
          %{"type" => "exit", "code" => 0}
        ]
      )

      write_run!(base_dir, "run-new",
        created_at_ms: 200,
        events: [
          %{"type" => "RUN_STARTED", "provider" => "fake"},
          %{"type" => "exit", "code" => 2}
        ]
      )

      assert [newer, older] = Runs.list()
      assert newer["run_id"] == "run-new"
      assert newer["status"] == "failed"
      assert newer["events"] == 2
      assert older["run_id"] == "run-old"
      assert older["status"] == "completed"
      assert older["provider"] == "fake"
      assert older["live"] == false
    end

    test "a run with no terminal exit event is interrupted", %{base_dir: base_dir} do
      write_run!(base_dir, "run-cut", events: [%{"type" => "RUN_STARTED"}])

      assert [%{"status" => "interrupted"}] = Runs.list()
    end

    test "a run with no events at all is empty", %{base_dir: base_dir} do
      write_run!(base_dir, "run-blank")

      assert [%{"status" => "empty", "events" => 0}] = Runs.list()
    end
  end

  describe "fetch/1" do
    test "returns manifest, events, and approvals", %{base_dir: base_dir} do
      write_run!(base_dir, "run-1",
        task: "fix tests",
        events: [
          %{"type" => "RUN_STARTED", "provider" => "fake"},
          %{"type" => "FINAL_OUTPUT", "text" => "done"},
          %{"type" => "exit", "code" => 0}
        ],
        approvals: [%{"type" => "approval_requested", "approval_id" => "a1"}]
      )

      assert {:ok, run} = Runs.fetch("run-1")
      assert run["run_id"] == "run-1"
      assert run["manifest"]["task"] == "fix tests"
      assert run["status"] == "completed"
      assert run["event_count"] == 3
      assert [%{"event" => %{"type" => "RUN_STARTED"}} | _] = run["events"]
      assert [%{"approval_id" => "a1"}] = run["approvals"]
    end

    test "rejects unknown and unsafe ids" do
      assert {:error, :not_found} = Runs.fetch("nope")
      assert {:error, :not_found} = Runs.fetch("../../etc")
      assert {:error, :not_found} = Runs.fetch(nil)
    end
  end

  describe "HTTP surface" do
    test "GET /runs lists run summaries", %{base_dir: base_dir} do
      write_run!(base_dir, "run-http", events: [%{"type" => "exit", "code" => 0}])

      conn = conn(:get, "/runs") |> Mitos.Web.Router.call([])

      assert conn.status == 200
      assert %{"runs" => [%{"run_id" => "run-http", "status" => "completed"}]} = Jason.decode!(conn.resp_body)
    end

    test "GET /runs/:id returns detail and 404s on unknown id", %{base_dir: base_dir} do
      write_run!(base_dir, "run-http", events: [%{"type" => "exit", "code" => 0}])

      conn = conn(:get, "/runs/run-http") |> Mitos.Web.Router.call([])
      assert conn.status == 200
      assert %{"run_id" => "run-http", "live" => false} = Jason.decode!(conn.resp_body)

      conn = conn(:get, "/runs/absent") |> Mitos.Web.Router.call([])
      assert conn.status == 404
    end
  end
end
