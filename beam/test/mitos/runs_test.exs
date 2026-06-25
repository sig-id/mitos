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

    test "ignores run directories with unsafe ids", %{base_dir: base_dir} do
      write_run!(base_dir, "run.bad")

      assert Runs.list() == []
    end

    test "ignores symlinked run directories", %{base_dir: base_dir} do
      runs_dir = Path.join(base_dir, ".runs")

      outside =
        Path.join(System.tmp_dir!(), "mitos-runs-outside-#{System.unique_integer([:positive])}")

      File.mkdir_p!(runs_dir)
      File.mkdir_p!(outside)
      File.write!(Path.join(outside, "run.yaml"), "run_id: run-linked\ncreated_at_ms: 200\n")

      File.write!(
        Path.join(outside, "events.jsonl"),
        Jason.encode!(%{"type" => "exit", "code" => 0}) <> "\n"
      )

      File.ln_s!(outside, Path.join(runs_dir, "run-linked"))

      on_exit(fn -> File.rm_rf!(outside) end)

      assert Runs.list() == []
      assert Runs.fetch("run-linked") == {:error, :not_found}
      assert Runs.events("run-linked") == {:error, :not_found}
    end

    test "ignores runs under a symlinked runs root", %{base_dir: base_dir} do
      outside =
        Path.join(
          System.tmp_dir!(),
          "mitos-runs-root-outside-#{System.unique_integer([:positive])}"
        )

      outside_runs = Path.join(outside, ".runs")

      File.mkdir_p!(base_dir)

      write_run!(outside, "run-runs-root-linked",
        created_at_ms: 250,
        events: [%{"type" => "exit", "code" => 0}]
      )

      File.ln_s!(outside_runs, Path.join(base_dir, ".runs"))

      on_exit(fn -> File.rm_rf!(outside) end)

      assert Runs.list() == []
      assert Runs.fetch("run-runs-root-linked") == {:error, :not_found}
      assert Runs.events("run-runs-root-linked") == {:error, :not_found}
    end

    test "ignores runs under a symlinked trace base" do
      outside =
        Path.join(
          System.tmp_dir!(),
          "mitos-runs-base-outside-#{System.unique_integer([:positive])}"
        )

      link =
        Path.join(System.tmp_dir!(), "mitos-runs-base-link-#{System.unique_integer([:positive])}")

      File.mkdir_p!(outside)

      write_run!(outside, "run-base-linked",
        created_at_ms: 300,
        events: [%{"type" => "exit", "code" => 0}]
      )

      File.ln_s!(outside, link)
      Application.put_env(:mitos, :trace_base_dir, link)

      on_exit(fn ->
        File.rm(link)
        File.rm_rf!(outside)
      end)

      assert Runs.list() == []
      assert Runs.fetch("run-base-linked") == {:error, :not_found}
      assert Runs.events("run-base-linked") == {:error, :not_found}
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

    test "canonical Decision and Effect tails derive status", %{base_dir: base_dir} do
      write_run!(base_dir, "run-finish",
        created_at_ms: 300,
        events: [
          %{"kind" => "decision", "type" => "finish", "seq" => 0}
        ]
      )

      write_run!(base_dir, "run-abort",
        created_at_ms: 200,
        events: [
          %{"kind" => "decision", "type" => "abort", "seq" => 0}
        ]
      )

      write_run!(base_dir, "run-cap",
        created_at_ms: 100,
        events: [
          %{"kind" => "effect", "type" => "capability_finished", "seq" => 1, "caused_by" => 0}
        ]
      )

      write_run!(base_dir, "run-cap-failed",
        created_at_ms: 50,
        events: [
          %{
            "kind" => "effect",
            "type" => "capability_finished",
            "seq" => 1,
            "caused_by" => 0,
            "payload" => %{"status" => "failed"}
          }
        ]
      )

      assert [finish, abort, cap, cap_failed] = Runs.list()
      assert finish["status"] == "completed"
      assert abort["status"] == "failed"
      assert cap["status"] == "completed"
      assert cap_failed["status"] == "failed"
    end

    test "post-finish proof effects do not override completed status", %{base_dir: base_dir} do
      write_run!(base_dir, "run-proofed",
        events: [
          %{"kind" => "effect", "type" => "capability_started", "seq" => 1, "caused_by" => 0},
          %{
            "kind" => "effect",
            "type" => "capability_finished",
            "seq" => 2,
            "caused_by" => 0,
            "payload" => %{"status" => "completed"}
          },
          %{
            "kind" => "effect",
            "type" => "budget_charged",
            "seq" => 3,
            "caused_by" => 0,
            "payload" => %{"amount_usdc" => "0.25"}
          }
        ]
      )

      assert [%{"status" => "completed"}] = Runs.list()
      assert {:ok, %{"status" => "completed"}} = Runs.fetch("run-proofed")
    end

    test "a run with no events at all is empty", %{base_dir: base_dir} do
      write_run!(base_dir, "run-blank")

      assert [%{"status" => "empty", "events" => 0}] = Runs.list()
    end
  end

  describe "fetch/1" do
    test "refuses unsafe run ids before reading trace files", %{base_dir: base_dir} do
      escaped_dir = Path.join(base_dir, "escaped")
      File.mkdir_p!(escaped_dir)
      File.write!(Path.join(escaped_dir, "run.yaml"), "run_id: escaped\n")

      File.write!(
        Path.join(escaped_dir, "events.jsonl"),
        Jason.encode!(%{"type" => "exit"}) <> "\n"
      )

      assert Runs.fetch("../escaped") == {:error, :not_found}
      assert Runs.events("../escaped") == {:error, :not_found}
      assert Runs.fetch("run.bad") == {:error, :not_found}
      assert Runs.events("run.bad") == {:error, :not_found}
    end

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

    test "does not read symlinked trace files from a regular run directory", %{
      base_dir: base_dir
    } do
      dir = write_run!(base_dir, "run-file-link")

      outside =
        Path.join(
          System.tmp_dir!(),
          "mitos-runs-file-outside-#{System.unique_integer([:positive])}"
        )

      File.mkdir_p!(outside)
      outside_events = Path.join(outside, "events.jsonl")
      File.write!(outside_events, Jason.encode!(%{"type" => "exit", "code" => 0}) <> "\n")
      File.rm!(Path.join(dir, "events.jsonl"))
      File.ln_s!(outside_events, Path.join(dir, "events.jsonl"))

      on_exit(fn -> File.rm_rf!(outside) end)

      assert [%{"run_id" => "run-file-link", "events" => 0, "status" => "empty"}] =
               Runs.list()

      assert {:ok, run} = Runs.fetch("run-file-link")
      assert run["events"] == []
      assert run["event_count"] == 0
      assert run["status"] == "empty"
      assert {:ok, []} = Runs.events("run-file-link")
    end

    test "proof endpoint returns buyer-safe proof without debug events", %{base_dir: base_dir} do
      write_run!(base_dir, "run-proof",
        events: [
          %{
            "kind" => "decision",
            "run_id" => "run-proof",
            "seq" => 0,
            "ts" => "2026-06-22T10:00:00.000Z",
            "actor" => "orchestrator",
            "type" => "advance",
            "params" => %{"prompt" => "seller recipe"},
            "rationale" => "secret rationale"
          },
          %{
            "kind" => "effect",
            "run_id" => "run-proof",
            "seq" => 1,
            "ts" => "2026-06-22T10:00:01.000Z",
            "type" => "capability_started",
            "caused_by" => 0,
            "ref" => %{"adapter" => "secret-harness"},
            "payload" => %{"harness" => "secret-harness"}
          },
          %{
            "kind" => "effect",
            "run_id" => "run-proof",
            "seq" => 2,
            "ts" => "2026-06-22T10:00:02.000Z",
            "type" => "egress_allowed",
            "caused_by" => 0,
            "ref" => %{
              "dest" => "api.example.test",
              "credential_id" => "cred-secret",
              "policy_rule" => "seller-private-policy"
            },
            "payload" => %{}
          },
          %{
            "kind" => "effect",
            "run_id" => "run-proof",
            "seq" => 3,
            "ts" => "2026-06-22T10:00:03.000Z",
            "type" => "capability_finished",
            "caused_by" => 0,
            "ref" => %{"result" => "RESULT"},
            "payload" => %{"status" => "completed", "result" => %{"signal" => "ship"}}
          }
        ]
      )

      conn = conn(:get, "/runs/run-proof/proof") |> Mitos.Web.Router.call([])
      assert conn.status == 200

      body = Jason.decode!(conn.resp_body)
      assert body["run_id"] == "run-proof"
      assert body["metering"]["egress_allowed_units"] == 1
      assert [%{"dest" => "api.example.test"}] = body["egress_crossings"]
      refute Map.has_key?(body, "events")
      refute inspect(body) =~ "secret rationale"
      refute inspect(body) =~ "seller recipe"
      refute inspect(body) =~ "secret-harness"
      refute inspect(body) =~ "cred-secret"
      refute inspect(body) =~ "seller-private-policy"

      page = conn(:get, "/runs/run-proof/proof.html") |> Mitos.Web.Router.call([])
      assert page.status == 200

      assert Enum.any?(page.resp_headers, fn {key, value} ->
               key == "content-type" and value =~ "text/html"
             end)

      assert page.resp_body =~ "Run Proof"
      assert page.resp_body =~ "api.example.test"
      assert page.resp_body =~ "ship"
      assert page.resp_body =~ "Effect Log Hash"
      refute page.resp_body =~ "secret rationale"
      refute page.resp_body =~ "seller recipe"
      refute page.resp_body =~ "secret-harness"
      refute page.resp_body =~ "cred-secret"
      refute page.resp_body =~ "seller-private-policy"
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

      assert %{"runs" => [%{"run_id" => "run-http", "status" => "completed"}]} =
               Jason.decode!(conn.resp_body)
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
