defmodule Mitos.AppRunnerTest do
  use ExUnit.Case, async: false

  setup do
    root = unique_tmp_dir("app-runner-app")
    traces = unique_tmp_dir("app-runner-traces")
    workloads = unique_tmp_dir("app-runner-workloads")

    File.mkdir_p!(Path.join(root, "pipelines"))
    File.mkdir_p!(Path.join([root, "capabilities", "ship"]))
    File.mkdir_p!(traces)
    File.mkdir_p!(workloads)

    previous_workloads_dir = Application.get_env(:mitos, :workloads_dir)
    Application.put_env(:mitos, :workloads_dir, workloads)

    on_exit(fn ->
      restore_env(:workloads_dir, previous_workloads_dir)
      writable_rm_rf(root)
      writable_rm_rf(traces)
      writable_rm_rf(workloads)
    end)

    {:ok, root: root, traces: traces, workloads: workloads}
  end

  test "runs a local app entrypoint through its bundled markdown pipeline", %{
    root: root,
    traces: traces
  } do
    write_app!(root)

    assert {:ok, result} =
             Mitos.AppRunner.run(
               root,
               "ship",
               %{"objective" => "from app", "name" => "Ada", "count" => 3},
               task_id: "app-task-1",
               trace_base_dir: traces,
               tenant: %{"org" => "buyer", "project" => "demo", "actor" => "operator"},
               source: "manual"
             )

    assert result["app_id"] =~ ~r/^app:sha256:[0-9a-f]{64}$/
    assert result["app_run_id"] =~ "app-"
    assert result["task_id"] == "app-task-1"
    assert result["entrypoint"] == "ship"
    assert result["pipeline_id"] == "ship-flow"
    assert result["status"] == "completed"
    assert result["deliverable_summary"] =~ "FAKE_FINAL: App from app for Ada count 3"
    assert result["evidence"]["app_run_id"] == result["app_run_id"]
    assert [%{"receipt" => %{"sink" => "web", "status" => "sent"}}] = result["deliveries"]

    assert [
             %{
               "id" => "ship-cap",
               "name" => "ship-cap",
               "capability_id" => "ship-cap",
               "run_id" => step_run_id,
               "status" => "completed"
             }
           ] = result["pipeline"]["steps"]

    assert is_binary(step_run_id)

    app_events = Mitos.Trace.read_events(result["app_run_id"], base_dir: traces)

    assert Enum.any?(app_events, fn
             %{
               "kind" => "effect",
               "type" => "signal_received",
               "ref" => %{"connector" => "manual", "task_id" => "app-task-1"},
               "payload" => %{"intake" => %{"input_keys" => ["count", "name", "objective"]}}
             } ->
               true

             _event ->
               false
           end)

    task_done_seq =
      Enum.find_value(app_events, fn
        %{
          "kind" => "effect",
          "type" => "task_done",
          "ref" => %{"task_id" => "app-task-1"},
          "seq" => seq
        } ->
          seq

        _event ->
          nil
      end)

    assert is_integer(task_done_seq)

    assert Enum.any?(app_events, fn
             %{
               "kind" => "effect",
               "type" => "delivery_attempted",
               "ref" => %{"sink" => "web", "task_id" => "app-task-1"},
               "caused_by" => ^task_done_seq
             } ->
               true

             _event ->
               false
           end)

    assert Enum.any?(app_events, fn
             %{
               "kind" => "effect",
               "type" => "delivery_sent",
               "ref" => %{"sink" => "web", "task_id" => "app-task-1"},
               "caused_by" => ^task_done_seq,
               "payload" => %{
                 "receipt" => %{"status" => "sent"},
                 "rendered" => %{"summary" => summary}
               }
             }
             when is_binary(summary) ->
               String.contains?(summary, "FAKE_FINAL: App from app")

             _event ->
               false
           end)

    app_pool = Mitos.WorkPool.fold_events(app_events)
    app_task = get_in(app_pool, ["tasks", "app-task-1"])

    assert app_task["status"] == "done"
    assert app_task["pipeline"] == "ship-flow"
    assert app_task["source"] == "manual"
    assert app_task["tenant"] == %{"org" => "buyer", "project" => "demo", "actor" => "operator"}
    assert app_task["provenance"]["app_id"] == result["app_id"]
    assert app_task["provenance"]["entrypoint"] == "ship"

    step_events = Mitos.Trace.read_events(step_run_id, base_dir: traces)
    step_pool = Mitos.WorkPool.fold_events(step_events)
    step_task = get_in(step_pool, ["tasks", "app-task-1:ship-cap"])

    assert step_task["status"] == "done"
    assert step_task["pipeline"] == "app:#{result["app_id"]}:ship-flow"
    assert step_task["payload"]["workload_id"] == "ship-cap"
  end

  test "rejects invalid entrypoint inputs before launching a capability", %{
    root: root,
    traces: traces
  } do
    write_app!(root)

    assert {:error, {:invalid_application_input, "missing required input objective"}} =
             Mitos.AppRunner.run(root, "ship", %{"name" => "Ada"},
               task_id: "app-task-invalid",
               trace_base_dir: traces
             )

    runs_dir = Path.join(traces, ".runs")
    assert not File.exists?(runs_dir) or File.ls!(runs_dir) == []
  end

  test "delivery trace public binding recursively strips credential-shaped fields", %{
    root: root,
    traces: traces
  } do
    write_app!(root)

    assert {:ok, result} =
             Mitos.AppRunner.run(
               root,
               "ship",
               %{"objective" => "from app", "name" => "Ada", "count" => 3},
               task_id: "app-task-redacted-delivery",
               trace_base_dir: traces,
               io_outputs: [
                 %{
                   sink: "webhook",
                   entrypoint: "ship",
                   on: "run.done",
                   url: "https://hooks.example.test/deliver",
                   token: "top-level-token",
                   headers: %{
                     :authorization => "Bearer nested-secret",
                     "x-public" => "visible"
                   },
                   payload: %{
                     include: ["result"],
                     client_secret: "payload-secret",
                     nested: [%{api_key: "nested-key", keep: "visible"}]
                   }
                 }
               ]
             )

    assert [%{"receipt" => %{"sink" => "webhook", "status" => "sent"}}] = result["deliveries"]

    app_events = Mitos.Trace.read_events(result["app_run_id"], base_dir: traces)

    attempted =
      Enum.find(app_events, fn
        %{
          "kind" => "effect",
          "type" => "delivery_attempted",
          "ref" => %{"task_id" => "app-task-redacted-delivery"}
        } ->
          true

        _event ->
          false
      end)

    assert get_in(attempted, ["payload", "binding"]) == %{
             "entrypoint" => "ship",
             "headers" => %{"x-public" => "visible"},
             "on" => "run.done",
             "payload" => %{
               "include" => ["result"],
               "nested" => [%{"keep" => "visible"}]
             },
             "sink" => "webhook",
             "url" => "https://hooks.example.test/deliver"
           }

    refute inspect(attempted) =~ "top-level-token"
    refute inspect(attempted) =~ "nested-secret"
    refute inspect(attempted) =~ "payload-secret"
    refute inspect(attempted) =~ "nested-key"
  end

  test "public io binding helper handles atom keys and nested lists" do
    assert Mitos.IOPlane.public_binding(%{
             sink: "webhook",
             token: "top-level-token",
             headers: %{:authorization => "Bearer nested-secret", "x-public" => "visible"},
             destinations: [
               %{credential_id: "cred-1", channel: "ops"},
               %{private_key: "pem", channel: "audit"}
             ]
           }) == %{
             "destinations" => [%{"channel" => "ops"}, %{"channel" => "audit"}],
             "headers" => %{"x-public" => "visible"},
             "sink" => "webhook"
           }
  end

  test "resolves content-addressed capability refs from the local workload catalog", %{
    root: root,
    traces: traces,
    workloads: workloads
  } do
    File.write!(
      Path.join(workloads, "catalog-ship.json"),
      Jason.encode!(%{
        provider: "fake",
        template: "Catalog {{objective}} for {{name}}",
        capability: %{name: "catalog-ship", harnesses: ["fake"], trust_tier: "T1"}
      })
    )

    assert {:ok, catalog_workload} = Mitos.Workload.fetch("catalog-ship")
    cap_ref = catalog_workload.capability.id

    write_app!(root, cap_ref: cap_ref)

    assert {:ok, result} =
             Mitos.AppRunner.run(
               root,
               "ship",
               %{"objective" => "catalog app", "name" => "Bea"},
               task_id: "app-task-cap-ref",
               trace_base_dir: traces
             )

    assert result["deliverable_summary"] =~ "FAKE_FINAL: Catalog catalog app for Bea"

    assert [
             %{
               "id" => "ship-cap",
               "name" => "ship-cap",
               "capability_id" => "catalog-ship",
               "run_id" => step_run_id
             }
           ] = result["pipeline"]["steps"]

    step_events = Mitos.Trace.read_events(step_run_id, base_dir: traces)
    step_pool = Mitos.WorkPool.fold_events(step_events)
    step_task = get_in(step_pool, ["tasks", "app-task-cap-ref:ship-cap"])

    assert step_task["payload"]["workload_id"] == "catalog-ship"
  end

  test "resolves pinned OCI capability refs from the local workload catalog", %{
    root: root,
    traces: traces,
    workloads: workloads
  } do
    image_ref = "oci://registry.test/catalog-ship@sha256:abc123"

    File.write!(
      Path.join(workloads, "oci-ship.json"),
      Jason.encode!(%{
        provider: "fake",
        template: "OCI {{objective}} for {{name}}",
        capability: %{
          name: "oci-ship",
          image: image_ref,
          harnesses: ["fake"],
          trust_tier: "T1"
        }
      })
    )

    write_app!(root, cap_ref: image_ref)

    assert {:ok, result} =
             Mitos.AppRunner.run(
               root,
               "ship",
               %{"objective" => "catalog app", "name" => "Cy"},
               task_id: "app-task-oci-ref",
               trace_base_dir: traces
             )

    assert result["deliverable_summary"] =~ "FAKE_FINAL: OCI catalog app for Cy"

    assert [
             %{
               "id" => "ship-cap",
               "name" => "ship-cap",
               "capability_id" => "oci-ship",
               "run_id" => step_run_id
             }
           ] = result["pipeline"]["steps"]

    step_events = Mitos.Trace.read_events(step_run_id, base_dir: traces)
    step_pool = Mitos.WorkPool.fold_events(step_events)
    step_task = get_in(step_pool, ["tasks", "app-task-oci-ref:ship-cap"])

    assert step_task["payload"]["workload_id"] == "oci-ship"
  end

  test "rejects mutable OCI capability refs", %{root: root, traces: traces} do
    write_app!(root, cap_ref: "oci://registry.test/catalog-ship:latest")

    assert {:error,
            {:app_run_failed, _run_id,
             {:pipeline_step_failed, "ship-cap", "ship-cap",
              {:unsupported_app_capability_ref, "oci://registry.test/catalog-ship:latest"}}}} =
             Mitos.AppRunner.run(
               root,
               "ship",
               %{"objective" => "mutable oci", "name" => "Dee"},
               task_id: "app-task-oci-mutable",
               trace_base_dir: traces
             )
  end

  test "rejects local capability refs to application control files", %{root: root, traces: traces} do
    write_app!(root, cap_ref: "app.json")

    assert {:error,
            {:app_run_failed, _run_id,
             {:pipeline_step_failed, "ship-cap", "ship-cap",
              {:invalid_application,
               "capability refs must not point at application control files"}}}} =
             Mitos.AppRunner.run(
               root,
               "ship",
               %{"objective" => "bad ref", "name" => "Dee"},
               task_id: "app-task-control-ref",
               trace_base_dir: traces
             )
  end

  defp write_app!(root, opts \\ []) do
    cap_ref = Keyword.get(opts, :cap_ref, "capabilities/ship/workload.json")

    File.write!(Path.join(root, "pipelines/ship.md"), """
    # Pipeline: ship-flow

    Goal: produce a backend application result.

    ## Stages
    1. **ship-cap** - run the local capability package.
    """)

    File.write!(
      Path.join(root, "capabilities/ship/workload.json"),
      Jason.encode!(%{
        provider: "fake",
        template:
          "App {{objective}} for {{name}} count {{count}} step {{pipeline_step}} previous {{previous_results}}",
        capability: %{name: "ship-cap", harnesses: ["fake"], trust_tier: "T1"}
      })
    )

    File.write!(
      Path.join(root, "app.json"),
      Jason.encode!(%{
        name: "ship-app",
        version: "1.0.0",
        capabilities: [
          %{ref: cap_ref, as: "ship-cap"}
        ],
        pipelines: [
          %{id: "ship-flow", file: "pipelines/ship.md"}
        ],
        entrypoints: [
          %{
            id: "ship",
            pipeline: "ship-flow",
            title: "Ship app output",
            workspace: "none",
            inputs: %{
              schema: %{
                type: "object",
                required: ["objective"],
                additionalProperties: false,
                properties: %{
                  objective: %{type: "string"},
                  name: %{type: "string"},
                  count: %{type: "int"}
                }
              }
            },
            defaults: %{budget: %{usd: 1, wall_ms: 30_000}}
          }
        ],
        io: %{inputs: [], outputs: []},
        services: %{requires: [], wants: []}
      })
    )
  end

  defp unique_tmp_dir(prefix) do
    path = Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
    File.rm_rf!(path)
    path
  end

  defp writable_rm_rf(path) do
    if File.exists?(path) do
      path
      |> Path.join("**/*")
      |> Path.wildcard(match_dot: true)
      |> Enum.each(fn entry ->
        case File.stat(entry) do
          {:ok, %{type: :directory, mode: mode}} -> File.chmod(entry, Bitwise.bor(mode, 0o700))
          {:ok, %{mode: mode}} -> File.chmod(entry, Bitwise.bor(mode, 0o600))
          {:error, _reason} -> :ok
        end
      end)

      File.chmod(path, 0o700)
      File.rm_rf!(path)
    end
  end

  defp restore_env(key, nil), do: Application.delete_env(:mitos, key)
  defp restore_env(key, value), do: Application.put_env(:mitos, key, value)
end
