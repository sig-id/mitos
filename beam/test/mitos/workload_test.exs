defmodule Mitos.WorkloadTest do
  use ExUnit.Case, async: false

  import Plug.Test
  import Plug.Conn

  alias Mitos.Workload

  setup do
    tmp = Path.join(System.tmp_dir!(), "mitos-workloads-#{System.unique_integer([:positive])}")
    File.mkdir_p!(tmp)
    previous = Application.get_env(:mitos, :workloads_dir)
    Application.put_env(:mitos, :workloads_dir, tmp)

    on_exit(fn ->
      if previous do
        Application.put_env(:mitos, :workloads_dir, previous)
      else
        Application.delete_env(:mitos, :workloads_dir)
      end

      File.rm_rf!(tmp)
    end)

    {:ok, dir: tmp}
  end

  defp write_workload!(dir, id, json) do
    File.write!(Path.join(dir, id <> ".json"), Jason.encode!(json))
  end

  describe "fetch/1" do
    test "loads a definition with defaults", %{dir: dir} do
      write_workload!(dir, "echo", %{provider: "fake", template: "{{objective}}"})

      assert {:ok, workload} = Workload.fetch("echo")
      assert workload.id == "echo"
      assert workload.provider == "fake"
      assert workload.transport_order == ["stdio-json", "pty"]
      assert workload.timeout_ms == 120_000
      assert workload.approvals == "deny"
    end

    test "strips // comment lines before decoding", %{dir: dir} do
      File.write!(
        Path.join(dir, "annotated.json"),
        "// operator note\n{\"provider\": \"fake\", \"template\": \"hi\"}\n"
      )

      assert {:ok, %Workload{provider: "fake"}} = Workload.fetch("annotated")
    end

    test "rejects ids that are not plain filename stems" do
      assert {:error, :not_found} = Workload.fetch("../escape")
      assert {:error, :not_found} = Workload.fetch("a/b")
      assert {:error, :not_found} = Workload.fetch(".hidden")
    end

    test "missing file is not_found" do
      assert {:error, :not_found} = Workload.fetch("nope")
    end

    test "definition without provider or template is invalid", %{dir: dir} do
      write_workload!(dir, "bad", %{template: "x"})
      assert {:error, {:invalid_workload, _}} = Workload.fetch("bad")

      write_workload!(dir, "bad2", %{provider: "fake"})
      assert {:error, {:invalid_workload, _}} = Workload.fetch("bad2")
    end
  end

  describe "render/2" do
    test "substitutes bindings", %{dir: dir} do
      write_workload!(dir, "tpl", %{provider: "fake", template: "Do {{objective}} for {{ name }}"})
      {:ok, workload} = Workload.fetch("tpl")

      assert {:ok, "Do review for ben"} =
               Workload.render(workload, %{"objective" => "review", "name" => "ben"})
    end

    test "reports missing bindings", %{dir: dir} do
      write_workload!(dir, "tpl", %{provider: "fake", template: "{{a}} {{b}}"})
      {:ok, workload} = Workload.fetch("tpl")

      assert {:error, {:missing_bindings, missing}} = Workload.render(workload, %{"a" => "1"})
      assert missing == ["b"]
    end
  end

  describe "public_view/1" do
    test "exposes placeholders but never env", %{dir: dir} do
      write_workload!(dir, "secret", %{
        provider: "fake",
        template: "{{objective}}",
        env: %{"API_KEY" => "sekrit"},
        env_passthrough: ["OTHER_KEY"]
      })

      {:ok, workload} = Workload.fetch("secret")
      view = Workload.public_view(workload)

      assert view["placeholders"] == ["objective"]
      refute Map.has_key?(view, "env")
      refute inspect(view) =~ "sekrit"
    end
  end

  describe "HTTP gateway" do
    @moduletag :integration

    setup do
      binary = Path.expand("../../../zig-out/bin/mitos-term", __DIR__)

      unless File.exists?(binary) do
        flunk("mitos-term binary not built at #{binary} — run `zig build` from the repo root")
      end

      :ok
    end

    defp post_json(path, body) do
      conn(:post, path, Jason.encode!(body))
      |> put_req_header("content-type", "application/json")
      |> Mitos.Web.Router.call([])
    end

    test "lists workloads", %{dir: dir} do
      write_workload!(dir, "echo", %{provider: "fake", template: "{{objective}}"})

      conn = conn(:get, "/workloads") |> Mitos.Web.Router.call([])
      assert conn.status == 200

      body = Jason.decode!(conn.resp_body)
      assert [%{"id" => "echo", "provider" => "fake"}] = body["workloads"]
    end

    test "executes a gentix provider_execution_request through the fake provider", %{dir: dir} do
      write_workload!(dir, "echo", %{
        provider: "fake",
        template: "{{objective}}",
        transport_order: ["stdio-json"],
        timeout_ms: 30_000
      })

      request = %{
        schema_id: "gentix.provider_execution_request",
        schema_version: 1,
        task: %{
          task_id: "task_test_1",
          objective: "echo this objective",
          expected_output: "the same text"
        },
        capability: %{capability_id: "cap_demo_echo"},
        request: %{}
      }

      conn = post_json("/workloads/echo/execute", request)
      assert conn.status == 200

      body = Jason.decode!(conn.resp_body)
      # The fake adapter's FINAL_OUTPUT (marker included) is authoritative
      # over the codec's stripped final_answer_candidate.
      assert body["deliverable_summary"] == "FAKE_FINAL: echo this objective"
      assert body["deliverable"]["result"] == "FAKE_FINAL: echo this objective"
      assert body["evidence"]["run_id"] =~ "wl-"
      assert body["execution_id"] == body["evidence"]["run_id"]
      assert body["model"] == "fake"
    end

    test "missing bindings yield 422", %{dir: dir} do
      write_workload!(dir, "needs-input", %{provider: "fake", template: "{{custom_field}}"})

      conn = post_json("/workloads/needs-input/execute", %{task: %{objective: "x"}})
      assert conn.status == 422
      assert Jason.decode!(conn.resp_body)["missing"] == ["custom_field"]
    end

    test "unknown workload yields 404" do
      conn = post_json("/workloads/missing/execute", %{})
      assert conn.status == 404
    end
  end
end
