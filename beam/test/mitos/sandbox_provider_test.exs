defmodule Mitos.SandboxProviderTest do
  use ExUnit.Case, async: false

  setup do
    previous_broker_endpoint = Application.get_env(:mitos, :broker_endpoint)
    previous_broker_ca_ref = Application.get_env(:mitos, :broker_ca_ref)

    on_exit(fn ->
      restore_env(:broker_endpoint, previous_broker_endpoint)
      restore_env(:broker_ca_ref, previous_broker_ca_ref)
    end)

    :ok
  end

  test "local sidecar provider exposes the canonical compute-plane callbacks" do
    provider = Mitos.SandboxProvider.LocalSidecar
    Code.ensure_loaded!(provider)

    for {name, arity} <- [
          prepare: 1,
          start: 3,
          stream: 1,
          control: 2,
          snapshot: 1,
          harvest: 1,
          stop: 1,
          capabilities: 0,
          child_spec: 1
        ] do
      assert function_exported?(provider, name, arity)
    end

    assert {:ok, %{provider: "fake"}} = provider.prepare(%{provider: "fake"})
    assert {:error, :missing_provider} = provider.start(%{}, [], %{})
    assert provider.stream(:session) == []

    assert {:ok, %{diff: %{"empty?" => true}, result: %{"signal" => "unknown"}, artifacts: []}} =
             provider.harvest(:session)

    assert %{
             isolation: :local_process,
             remote?: false,
             egress: :broker,
             data_mount: :bind,
             snapshot?: true,
             net_modes: [:host]
           } = provider.capabilities()
  end

  test "local sidecar provider executes through prepare start stream harvest stop callbacks" do
    provider = Mitos.SandboxProvider.LocalSidecar
    trace_base_dir = unique_tmp_dir("local-sidecar-provider-trace")
    session_id = "provider-" <> Integer.to_string(System.unique_integer([:positive]))

    on_exit(fn -> File.rm_rf!(trace_base_dir) end)

    {:ok, handle} =
      provider.prepare(%{
        provider: "fake",
        task: "provider contract smoke",
        session_id: session_id,
        trace_base_dir: trace_base_dir,
        transport_order: ["stdio-json"],
        idle_ms: 20
      })

    assert {:ok, session} = provider.start(handle, [], %{})

    events =
      session
      |> Map.put(:stream_timeout_ms, 5_000)
      |> provider.stream()
      |> Enum.to_list()

    assert Enum.any?(events, &match?(%{"type" => "RUN_STARTED", "provider" => "fake"}, &1))

    assert Enum.any?(events, fn
             %{"type" => "FINAL_OUTPUT", "text" => "FAKE_FINAL: provider contract smoke"} ->
               true

             _event ->
               false
           end)

    assert Enum.any?(events, &(Map.get(&1, "type") in ["PROCESS_EXITED", "exit"]))

    assert {:ok, harvest} = provider.harvest(session)
    assert harvest.result["signal"] == "unknown"

    assert :ok = provider.stop(session)

    trace_events = Mitos.Trace.read_events(session_id, base_dir: trace_base_dir)
    assert Enum.any?(trace_events, &(Map.get(&1, "kind") == "decision"))
    assert Enum.any?(trace_events, &(Map.get(&1, "type") == "capability_finished"))
  end

  test "sandbox policy allows local T1 but refuses T2 without a remote microVM provider" do
    Application.put_env(:mitos, :broker_endpoint, "https://broker.test")

    assert :ok = Mitos.SandboxPolicy.ensure_trust_tier_allowed("T1")

    assert {:error, {:sandbox_provider_ineligible, "T2", detail}} =
             Mitos.SandboxPolicy.ensure_trust_tier_allowed("T2")

    assert detail.reason =~ "remote microVM-class provider"
    assert [%{remote?: false, egress: :broker}] = detail.providers
  end

  test "sandbox policy refuses T2 when forced-proxy broker endpoint is not configured" do
    previous_providers = Application.get_env(:mitos, :sandbox_providers)

    Application.put_env(:mitos, :broker_endpoint, "")

    Application.put_env(:mitos, :sandbox_providers, [
      %{
        isolation: :microvm,
        remote?: true,
        egress: :broker,
        data_mount: :snapshot,
        metering: [:cpu, :mem, :wall, :egress],
        net_modes: [:broker_proxy]
      }
    ])

    on_exit(fn -> restore_env(:sandbox_providers, previous_providers) end)

    assert {:error, {:sandbox_provider_ineligible, "T2", detail}} =
             Mitos.SandboxPolicy.ensure_trust_tier_allowed("T2")

    assert detail.reason =~ "broker endpoint"
  end

  test "sandbox policy accepts T2 when a configured provider advertises remote microVM isolation" do
    previous = Application.get_env(:mitos, :sandbox_providers)

    Application.put_env(:mitos, :broker_endpoint, "https://broker.test")

    Application.put_env(:mitos, :sandbox_providers, [
      %{
        isolation: :microvm,
        remote?: true,
        egress: :broker,
        data_mount: :snapshot,
        metering: [:cpu, :mem, :wall, :egress],
        net_modes: [:broker_proxy]
      }
    ])

    on_exit(fn ->
      if previous do
        Application.put_env(:mitos, :sandbox_providers, previous)
      else
        Application.delete_env(:mitos, :sandbox_providers)
      end
    end)

    assert :ok = Mitos.SandboxPolicy.ensure_trust_tier_allowed("T2")
  end

  test "sandbox policy refuses T2 when provider lacks required billing metering" do
    previous = Application.get_env(:mitos, :sandbox_providers)

    Application.put_env(:mitos, :broker_endpoint, "https://broker.test")

    Application.put_env(:mitos, :sandbox_providers, [
      %{
        isolation: :microvm,
        remote?: true,
        egress: :broker,
        data_mount: :snapshot,
        metering: [:cpu, :wall],
        net_modes: [:broker_proxy]
      }
    ])

    on_exit(fn -> restore_env(:sandbox_providers, previous) end)

    assert {:error, {:sandbox_provider_ineligible, "T2", detail}} =
             Mitos.SandboxPolicy.ensure_trust_tier_allowed("T2")

    assert detail.reason =~ "CPU/mem/wall/egress metering"
    assert [%{metering: [:cpu, :wall]}] = detail.providers
  end

  test "sandbox policy selects built-in remote microvm provider when endpoint is configured" do
    previous_providers = Application.get_env(:mitos, :sandbox_providers)
    previous_endpoint = Application.get_env(:mitos, :remote_microvm_endpoint)

    Application.delete_env(:mitos, :sandbox_providers)
    Application.put_env(:mitos, :broker_endpoint, "https://broker.test")
    Application.put_env(:mitos, :remote_microvm_endpoint, "http://remote-microvm.test")

    on_exit(fn ->
      if previous_providers do
        Application.put_env(:mitos, :sandbox_providers, previous_providers)
      else
        Application.delete_env(:mitos, :sandbox_providers)
      end

      if previous_endpoint do
        Application.put_env(:mitos, :remote_microvm_endpoint, previous_endpoint)
      else
        Application.delete_env(:mitos, :remote_microvm_endpoint)
      end
    end)

    {:ok, workload} =
      Mitos.Workload.from_json("remote-default", %{
        "provider" => "fake",
        "template" => "{{objective}}",
        "capability" => %{
          "name" => "remote-default",
          "harnesses" => ["fake"],
          "trust_tier" => "T2",
          "result" => %{"path" => "RESULT"}
        }
      })

    assert :ok = Mitos.SandboxPolicy.ensure_trust_tier_allowed("T2")

    assert {:ok, Mitos.SandboxProvider.RemoteMicrovm} =
             Mitos.SandboxPolicy.select_workload_provider(workload)
  end

  test "remote microvm provider advertises T2-compatible capabilities" do
    assert %{
             isolation: :microvm,
             remote?: true,
             egress: :broker,
             data_mount: :snapshot,
             metering: [:cpu, :mem, :wall, :egress],
             net_modes: [:broker_proxy]
           } = Mitos.SandboxProvider.RemoteMicrovm.capabilities()
  end

  test "remote microvm provider refuses malformed T2 launch specs before start" do
    previous_endpoint = Application.get_env(:mitos, :remote_microvm_endpoint)
    Application.put_env(:mitos, :remote_microvm_endpoint, "http://remote-microvm.test")
    Application.put_env(:mitos, :broker_endpoint, "https://broker.test/proxy")

    on_exit(fn -> restore_env(:remote_microvm_endpoint, previous_endpoint) end)

    assert {:error, {:invalid_t2_launch_spec, "net_mode must be deny_all_except_broker"}} =
             Mitos.SandboxProvider.RemoteMicrovm.prepare(%{
               provider: "fake",
               trust_tier: "T2",
               data: %{"snapshot" => "snap://data", "ro" => true},
               posture: %{}
             })

    invalid_data =
      valid_t2_launch_spec()
      |> Map.put(:data, %{"path" => "/host/data", "ro" => true})

    assert {:error, {:invalid_t2_launch_spec, "T2 data must not use host bind/path mounts"}} =
             Mitos.SandboxProvider.RemoteMicrovm.prepare(invalid_data)

    assert {:ok, _no_data_handle} =
             valid_t2_launch_spec()
             |> Map.delete(:data)
             |> Mitos.SandboxProvider.RemoteMicrovm.prepare()

    invalid_workspace =
      valid_t2_launch_spec()
      |> Map.put(:workspace, %{"path" => "/host/workspace", "ro" => true})

    assert {:error, {:invalid_t2_launch_spec, "T2 workspace must not use host bind/path mounts"}} =
             Mitos.SandboxProvider.RemoteMicrovm.prepare(invalid_workspace)

    invalid_cwd =
      valid_t2_launch_spec()
      |> Map.put(:cwd, "/host/workspace")

    assert {:error, {:invalid_t2_launch_spec, "T2 cwd must be provider-internal"}} =
             Mitos.SandboxProvider.RemoteMicrovm.prepare(invalid_cwd)

    assert {:ok, handle} = Mitos.SandboxProvider.RemoteMicrovm.prepare(valid_t2_launch_spec())

    assert {:error, {:invalid_t2_launch_spec, "T2 starts must not inject env"}} =
             Mitos.SandboxProvider.RemoteMicrovm.start(handle, [], %{"SECRET" => "bad"})
  end

  test "remote microvm provider returns provider-supplied snapshots" do
    assert {:ok,
            %{
              "id" => "snap-1",
              "sha256" => "sha256:abc",
              "metadata" => %{"provider" => "test"}
            }} =
             Mitos.SandboxProvider.RemoteMicrovm.snapshot(%{
               snapshot: %{
                 id: "snap-1",
                 sha256: "sha256:abc",
                 metadata: %{provider: "test"}
               }
             })

    assert {:error, :snapshot_missing} = Mitos.SandboxProvider.RemoteMicrovm.snapshot(%{})
  end

  test "remote microvm provider normalizes preloaded stream events" do
    assert [%{"type" => "FINAL_OUTPUT", "text" => "done"}] =
             Mitos.SandboxProvider.RemoteMicrovm.stream(%{
               events: [%{type: "FINAL_OUTPUT", text: "done"}]
             })
  end

  test "remote microvm provider drives lifecycle through HTTP service" do
    parent = self()
    previous_endpoint = Application.get_env(:mitos, :remote_microvm_endpoint)

    endpoint =
      start_remote_provider_server(fn
        "POST", "/runs", body ->
          send(parent, {:remote_provider_request, "POST", "/runs", body})

          {200,
           %{
             "run_id" => "remote-1",
             "events" => [%{type: "FINAL_OUTPUT", text: "remote done"}]
           }}

        "POST", "/runs/remote-1/snapshot", body ->
          send(parent, {:remote_provider_request, "POST", "/runs/remote-1/snapshot", body})
          {200, %{"snapshot" => %{"id" => "snap-remote", "sha256" => "sha256:remote"}}}

        "POST", "/runs/remote-1/harvest", body ->
          send(parent, {:remote_provider_request, "POST", "/runs/remote-1/harvest", body})

          {200,
           %{
             "harvest" => %{
               "diff" => %{"empty?" => true, "files_changed" => []},
               "result" => %{"signal" => "ship", "summary" => "done"},
               "artifacts" => []
             }
           }}

        "POST", "/runs/remote-1/stop", body ->
          send(parent, {:remote_provider_request, "POST", "/runs/remote-1/stop", body})
          {200, %{"ok" => true}}

        method, path, body ->
          send(parent, {:remote_provider_request, method, path, body})
          {404, %{"error" => "unexpected route"}}
      end)

    Application.put_env(:mitos, :remote_microvm_endpoint, endpoint)
    on_exit(fn -> restore_env(:remote_microvm_endpoint, previous_endpoint) end)

    assert {:ok, handle} =
             Mitos.SandboxProvider.RemoteMicrovm.prepare(%{
               provider: "fake",
               run_id: "local-run"
             })

    assert {:ok, session} =
             Mitos.SandboxProvider.RemoteMicrovm.start(handle, ["run"], %{"SAFE" => "1"})

    assert session.remote_run_id == "remote-1"

    assert Mitos.SandboxProvider.RemoteMicrovm.stream(session) == [
             %{"type" => "FINAL_OUTPUT", "text" => "remote done"}
           ]

    assert_receive {:remote_provider_request, "POST", "/runs",
                    %{
                      "argv" => ["run"],
                      "env" => %{"SAFE" => "1"},
                      "launch_spec" => %{"provider" => "fake", "run_id" => "local-run"}
                    }}

    assert {:ok, %{"id" => "snap-remote", "sha256" => "sha256:remote"}} =
             Mitos.SandboxProvider.RemoteMicrovm.snapshot(session)

    assert_receive {:remote_provider_request, "POST", "/runs/remote-1/snapshot", %{}}

    assert {:ok, harvest} = Mitos.SandboxProvider.RemoteMicrovm.harvest(session)
    assert harvest.result["signal"] == "ship"
    assert harvest.result["summary"] == "done"

    assert_receive {:remote_provider_request, "POST", "/runs/remote-1/harvest", %{}}

    assert :ok = Mitos.SandboxProvider.RemoteMicrovm.stop(session)
    assert_receive {:remote_provider_request, "POST", "/runs/remote-1/stop", %{}}
  end

  test "remote microvm provider refuses start responses without a remote run id" do
    parent = self()

    endpoint =
      start_remote_provider_server(fn method, path, body ->
        send(parent, {:remote_provider_request, method, path, body})
        {200, %{"events" => [%{"type" => "RUN_STARTED"}]}}
      end)

    previous_endpoint = Application.get_env(:mitos, :remote_microvm_endpoint)
    Application.put_env(:mitos, :remote_microvm_endpoint, endpoint)
    on_exit(fn -> restore_env(:remote_microvm_endpoint, previous_endpoint) end)

    {:ok, handle} = Mitos.SandboxProvider.RemoteMicrovm.prepare(%{provider: "fake"})

    assert {:error, :missing_remote_run_id} =
             Mitos.SandboxProvider.RemoteMicrovm.start(handle, [], %{})

    assert_receive {:remote_provider_request, "POST", "/runs", _body}
  end

  test "remote microvm provider requires T2 broker-only egress attestation before start succeeds" do
    parent = self()

    endpoint =
      start_remote_provider_server(fn
        "POST", "/runs", %{"launch_spec" => %{"run_id" => "missing-attestation"}} = body ->
          send(parent, {:remote_provider_request, "POST", "/runs", body})
          {200, %{"run_id" => "remote-missing-attestation"}}

        "POST", "/runs", %{"launch_spec" => %{"run_id" => "bad-attestation"}} = body ->
          send(parent, {:remote_provider_request, "POST", "/runs", body})

          {200,
           %{
             "run_id" => "remote-bad-attestation",
             "egress_attestation" => %{
               "enforced" => false,
               "egress" => "broker",
               "net_mode" => "deny_all_except_broker",
               "deny_all_else" => true,
               "host_network" => false,
               "loopback_bypass" => false,
               "mechanism" => "forced_proxy",
               "broker_endpoint" => "https://broker.test/proxy"
             }
           }}

        "POST", "/runs", %{"launch_spec" => %{"run_id" => "good-attestation"}} = body ->
          send(parent, {:remote_provider_request, "POST", "/runs", body})

          {200,
           %{
             "run_id" => "remote-good-attestation",
             "egress_attestation" => valid_t2_egress_attestation(),
             "events" => [%{"type" => "RUN_STARTED"}]
           }}

        method, path, body ->
          send(parent, {:remote_provider_request, method, path, body})
          {404, %{"error" => "unexpected route"}}
      end)

    previous_endpoint = Application.get_env(:mitos, :remote_microvm_endpoint)
    Application.put_env(:mitos, :remote_microvm_endpoint, endpoint)
    Application.put_env(:mitos, :broker_endpoint, "https://broker.test/proxy")
    on_exit(fn -> restore_env(:remote_microvm_endpoint, previous_endpoint) end)

    {:ok, missing_handle} =
      valid_t2_launch_spec()
      |> Map.put(:run_id, "missing-attestation")
      |> Mitos.SandboxProvider.RemoteMicrovm.prepare()

    assert {:error, {:egress_attestation_required, _reason}} =
             Mitos.SandboxProvider.RemoteMicrovm.start(missing_handle, [], %{})

    {:ok, bad_handle} =
      valid_t2_launch_spec()
      |> Map.put(:run_id, "bad-attestation")
      |> Mitos.SandboxProvider.RemoteMicrovm.prepare()

    assert {:error, {:invalid_egress_attestation, "egress enforcement must be true"}} =
             Mitos.SandboxProvider.RemoteMicrovm.start(bad_handle, [], %{})

    {:ok, good_handle} =
      valid_t2_launch_spec()
      |> Map.put(:run_id, "good-attestation")
      |> Mitos.SandboxProvider.RemoteMicrovm.prepare()

    assert {:ok, session} = Mitos.SandboxProvider.RemoteMicrovm.start(good_handle, [], %{})
    assert session.remote_run_id == "remote-good-attestation"
    assert session.egress_attestation == valid_t2_egress_attestation()

    assert_receive {:remote_provider_request, "POST", "/runs",
                    %{"launch_spec" => %{"run_id" => "missing-attestation"}}}

    assert_receive {:remote_provider_request, "POST", "/runs",
                    %{"launch_spec" => %{"run_id" => "bad-attestation"}}}

    assert_receive {:remote_provider_request, "POST", "/runs",
                    %{"launch_spec" => %{"run_id" => "good-attestation"}}}
  end

  test "remote microvm provider streams events from cursor-paginated HTTP endpoint" do
    parent = self()

    endpoint =
      start_remote_provider_server(fn
        "POST", "/runs", body ->
          send(parent, {:remote_provider_request, "POST", "/runs", body})

          {200,
           %{
             "run_id" => "remote-stream-1",
             "event_cursor" => "0",
             "stream_poll_ms" => 1,
             "stream_max_empty_polls" => 2
           }}

        "POST", "/runs/remote-stream-1/events", %{"cursor" => "0"} ->
          send(parent, {:remote_provider_request, "POST", "/runs/remote-stream-1/events", "0"})

          {200,
           %{
             "events" => [%{"type" => "RUN_STARTED", "provider" => "fake"}],
             "next_cursor" => "1"
           }}

        "POST", "/runs/remote-stream-1/events", %{"cursor" => "1"} ->
          send(parent, {:remote_provider_request, "POST", "/runs/remote-stream-1/events", "1"})

          {200,
           %{
             "events" => [
               %{"type" => "FINAL_OUTPUT", "text" => "streamed"},
               %{"type" => "PROCESS_EXITED", "code" => 0}
             ],
             "next_cursor" => "2",
             "done" => true
           }}

        method, path, body ->
          send(parent, {:remote_provider_request, method, path, body})
          {404, %{"error" => "unexpected route"}}
      end)

    previous_endpoint = Application.get_env(:mitos, :remote_microvm_endpoint)
    Application.put_env(:mitos, :remote_microvm_endpoint, endpoint)
    on_exit(fn -> restore_env(:remote_microvm_endpoint, previous_endpoint) end)

    {:ok, handle} = Mitos.SandboxProvider.RemoteMicrovm.prepare(%{provider: "fake"})
    {:ok, session} = Mitos.SandboxProvider.RemoteMicrovm.start(handle, [], %{})

    assert Mitos.SandboxProvider.RemoteMicrovm.stream(session) |> Enum.to_list() == [
             %{"type" => "RUN_STARTED", "provider" => "fake"},
             %{"type" => "FINAL_OUTPUT", "text" => "streamed"},
             %{"type" => "PROCESS_EXITED", "code" => 0}
           ]

    assert_receive {:remote_provider_request, "POST", "/runs/remote-stream-1/events", "0"}
    assert_receive {:remote_provider_request, "POST", "/runs/remote-stream-1/events", "1"}
  end

  test "remote microvm provider percent-encodes opaque run ids in request paths" do
    parent = self()

    endpoint =
      start_remote_provider_server(fn method, path, body ->
        send(parent, {:remote_provider_request, method, path, body})

        cond do
          String.ends_with?(path, "/events") ->
            {200, %{"events" => [%{"type" => "PROCESS_EXITED", "code" => 0}], "done" => true}}

          String.ends_with?(path, "/snapshot") ->
            {200, %{"snapshot" => %{"id" => "snap-opaque"}}}

          String.ends_with?(path, "/harvest") ->
            {200, %{"harvest" => %{"diff" => %{}, "result" => %{}, "artifacts" => []}}}

          true ->
            {200, %{"ok" => true}}
        end
      end)

    session = %{
      endpoint: endpoint,
      remote_run_id: "remote/opaque id?#",
      event_cursor: "0",
      stream_poll_ms: 1,
      stream_max_empty_polls: 1,
      events: []
    }

    encoded = "remote%2Fopaque%20id%3F%23"

    assert :ok = Mitos.SandboxProvider.RemoteMicrovm.control(session, {:signal, 2})
    assert {:ok, %{"id" => "snap-opaque"}} = Mitos.SandboxProvider.RemoteMicrovm.snapshot(session)
    assert {:ok, _harvest} = Mitos.SandboxProvider.RemoteMicrovm.harvest(session)

    assert [%{"type" => "PROCESS_EXITED", "code" => 0}] =
             Mitos.SandboxProvider.RemoteMicrovm.stream(session) |> Enum.to_list()

    assert :ok = Mitos.SandboxProvider.RemoteMicrovm.stop(session)

    control_path = "/runs/#{encoded}/control"
    snapshot_path = "/runs/#{encoded}/snapshot"
    harvest_path = "/runs/#{encoded}/harvest"
    events_path = "/runs/#{encoded}/events"
    stop_path = "/runs/#{encoded}/stop"

    assert_receive {:remote_provider_request, "POST", ^control_path, _body}
    assert_receive {:remote_provider_request, "POST", ^snapshot_path, %{}}
    assert_receive {:remote_provider_request, "POST", ^harvest_path, %{}}
    assert_receive {:remote_provider_request, "POST", ^events_path, %{"cursor" => "0"}}
    assert_receive {:remote_provider_request, "POST", ^stop_path, %{}}
  end

  test "remote microvm provider forwards normalized control commands" do
    parent = self()

    endpoint =
      start_remote_provider_server(fn method, path, body ->
        send(parent, {:remote_provider_request, method, path, body})
        {200, %{"ok" => true}}
      end)

    session = %{endpoint: endpoint, remote_run_id: "remote-1"}

    assert :ok =
             Mitos.SandboxProvider.RemoteMicrovm.control(
               session,
               {:steer, :answer, text: "ship it"}
             )

    assert_receive {:remote_provider_request, "POST", "/runs/remote-1/control",
                    %{
                      "command" => %{
                        "cmd" => "steer",
                        "interaction" => "answer",
                        "text" => "ship it"
                      }
                    }}

    assert :ok =
             Mitos.SandboxProvider.RemoteMicrovm.control(session, {:write_stdin, <<0, "abc">>})

    assert_receive {:remote_provider_request, "POST", "/runs/remote-1/control",
                    %{
                      "command" => %{
                        "cmd" => "write_stdin",
                        "bytes" => encoded,
                        "encoding" => "base64"
                      }
                    }}

    assert Base.decode64!(encoded) == <<0, "abc">>
  end

  test "remote microvm provider returns control rejection errors" do
    endpoint =
      start_remote_provider_server(fn _method, _path, _body ->
        {200, %{"ok" => false, "error" => "session_not_live"}}
      end)

    assert {:error, "session_not_live"} =
             Mitos.SandboxProvider.RemoteMicrovm.control(
               %{endpoint: endpoint, remote_run_id: "remote-1"},
               {:steer, :interrupt, []}
             )

    assert {:error, :missing_remote_run_id} =
             Mitos.SandboxProvider.RemoteMicrovm.control(
               %{endpoint: endpoint},
               {:steer, :prompt, []}
             )
  end

  test "sandbox policy compiles T2 launch posture for remote microvm enforcement" do
    Application.put_env(:mitos, :broker_endpoint, "https://broker.test/proxy")
    Application.put_env(:mitos, :broker_ca_ref, "ca:mitos:test")

    posture =
      Mitos.SandboxPolicy.launch_posture("T2", %{
        resources: %{"timeout" => "900s", "cpu" => 2}
      })

    assert posture["trust_tier"] == "T2"
    assert posture["net_mode"] == "deny_all_except_broker"
    assert posture["egress"] == "broker"
    assert posture["host_network"] == false
    assert posture["loopback_bypass"] == false
    assert posture["broker_endpoint"] == "https://broker.test/proxy"
    assert posture["ca_ref"] == "ca:mitos:test"
    assert posture["data_mount"] == "snapshot"
    assert posture["metering"] == "billing_ledger"
    assert posture["metered_dimensions"] == ["cpu", "mem", "wall", "egress"]
    assert posture["workspace"] == %{"mode" => "rw", "ephemeral" => true, "host_bind" => false}

    assert posture["data"] == %{
             "mode" => "ro",
             "source" => "snapshot_or_volume",
             "host_bind" => false
           }

    assert posture["timeouts"] == %{"wall" => "900s"}
  end

  defp restore_env(key, nil), do: Application.delete_env(:mitos, key)
  defp restore_env(key, value), do: Application.put_env(:mitos, key, value)

  test "local sidecar provider harvests workspace diff, RESULT, and registered artifacts" do
    provider = Mitos.SandboxProvider.LocalSidecar
    root = unique_tmp_dir("harvest")
    workspace = Path.join(root, "workspace")
    trace_dir = Path.join(root, "trace")
    File.mkdir_p!(workspace)
    File.mkdir_p!(trace_dir)
    on_exit(fn -> File.rm_rf!(root) end)

    git!(workspace, ["init"])
    git!(workspace, ["config", "user.email", "test@example.invalid"])
    git!(workspace, ["config", "user.name", "Mitos Test"])
    File.write!(Path.join(workspace, "tracked.txt"), "base\n")
    git!(workspace, ["add", "tracked.txt"])
    git!(workspace, ["commit", "-m", "base"])

    File.write!(Path.join(workspace, "tracked.txt"), "changed\n")

    File.write!(
      Path.join(workspace, "RESULT"),
      Jason.encode!(%{
        signal: "ship",
        confidence: 0.75,
        summary: "done",
        evidence: []
      })
    )

    File.mkdir_p!(Path.join(workspace, "screenshots"))
    File.write!(Path.join(workspace, "screenshots/one.txt"), "artifact")

    File.write!(
      Path.join(trace_dir, "events.jsonl"),
      Jason.encode!(%{
        "event" => %{
          "type" => "ARTIFACT_REGISTERED",
          "kind" => "screenshot",
          "path" => "screenshots/one.txt"
        }
      }) <> "\n"
    )

    assert {:ok, harvest} =
             provider.harvest(%{
               cwd: workspace,
               trace_dir: trace_dir,
               launch_spec: %{result: %{path: "RESULT"}}
             })

    assert harvest.diff["files_changed"] == ["RESULT", "screenshots/one.txt", "tracked.txt"]
    assert harvest.diff["patch"] =~ "-base"
    assert harvest.diff["patch"] =~ "+changed"
    assert harvest.diff["sha256"] =~ "sha256:"
    assert File.read!(Path.join(trace_dir, "diff.patch")) == harvest.diff["patch"]

    assert Jason.decode!(File.read!(Path.join(trace_dir, "files_changed.json"))) == [
             "RESULT",
             "screenshots/one.txt",
             "tracked.txt"
           ]

    assert harvest.result["signal"] == "ship"
    assert harvest.result["summary"] == "done"
    assert harvest.result["path"] == "RESULT"
    assert harvest.result["synthetic"] == false
    assert harvest.result["sha256"] =~ "sha256:"

    assert [
             %{
               "kind" => "screenshot",
               "path" => "screenshots/one.txt",
               "sha256" => "sha256:" <> _,
               "bytes" => 8
             }
           ] = harvest.artifacts
  end

  defp git!(cwd, args) do
    case System.cmd("git", ["-C", cwd | args], stderr_to_stdout: true) do
      {_output, 0} -> :ok
      {output, status} -> flunk("git #{Enum.join(args, " ")} failed with #{status}: #{output}")
    end
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end

  defp valid_t2_launch_spec do
    launch_spec = %{
      provider: "fake",
      run_id: "remote-t2-test",
      trust_tier: "T2",
      data: %{"snapshot" => "snap://capability-data", "mount" => "/data", "ro" => true},
      resources: %{"timeout" => "60s"}
    }

    Map.put(
      launch_spec,
      :posture,
      Mitos.SandboxPolicy.launch_posture("T2", launch_spec)
    )
  end

  defp valid_t2_egress_attestation do
    %{
      "enforced" => true,
      "egress" => "broker",
      "net_mode" => "deny_all_except_broker",
      "deny_all_else" => true,
      "host_network" => false,
      "loopback_bypass" => false,
      "mechanism" => "forced_proxy",
      "broker_endpoint" => "https://broker.test/proxy"
    }
  end

  defp start_remote_provider_server(handler) when is_function(handler, 3) do
    {:ok, listen_socket} =
      :gen_tcp.listen(0, [
        :binary,
        packet: :raw,
        active: false,
        ip: {127, 0, 0, 1},
        reuseaddr: true
      ])

    {:ok, {{127, 0, 0, 1}, port}} = :inet.sockname(listen_socket)
    server = spawn_link(fn -> remote_provider_accept_loop(listen_socket, handler) end)

    on_exit(fn ->
      :gen_tcp.close(listen_socket)
      Process.exit(server, :shutdown)
    end)

    "http://127.0.0.1:#{port}"
  end

  defp remote_provider_accept_loop(listen_socket, handler) do
    case :gen_tcp.accept(listen_socket) do
      {:ok, socket} ->
        remote_provider_handle_socket(socket, handler)
        remote_provider_accept_loop(listen_socket, handler)

      {:error, :closed} ->
        :ok

      {:error, _reason} ->
        remote_provider_accept_loop(listen_socket, handler)
    end
  end

  defp remote_provider_handle_socket(socket, handler) do
    case read_http_request(socket) do
      {:ok, method, path, body} ->
        decoded_body = if body == "", do: %{}, else: Jason.decode!(body)
        {status, response} = remote_provider_response(handler.(method, path, decoded_body))
        send_http_response(socket, status, response)

      {:error, reason} ->
        send_http_response(socket, 500, %{"error" => inspect(reason)})
    end

    :gen_tcp.close(socket)
  end

  defp read_http_request(socket, acc \\ "") do
    case :gen_tcp.recv(socket, 0, 1_000) do
      {:ok, chunk} ->
        acc = acc <> chunk

        case parse_http_request(acc) do
          {:ok, method, path, body} -> {:ok, method, path, body}
          :more -> read_http_request(socket, acc)
        end

      {:error, reason} ->
        {:error, reason}
    end
  end

  defp parse_http_request(bytes) do
    case :binary.match(bytes, "\r\n\r\n") do
      {header_size, 4} ->
        header = binary_part(bytes, 0, header_size)
        body_offset = header_size + 4
        body = binary_part(bytes, body_offset, byte_size(bytes) - body_offset)
        [request_line | header_lines] = String.split(header, "\r\n")
        [method, path | _] = String.split(request_line, " ")
        content_length = content_length(header_lines)

        if byte_size(body) >= content_length do
          {:ok, method, path, binary_part(body, 0, content_length)}
        else
          :more
        end

      :nomatch ->
        :more
    end
  end

  defp content_length(header_lines) do
    Enum.find_value(header_lines, 0, fn line ->
      case String.split(line, ":", parts: 2) do
        [name, value] ->
          if String.downcase(name) == "content-length" do
            value |> String.trim() |> String.to_integer()
          end

        _other ->
          nil
      end
    end)
  end

  defp remote_provider_response({status, %{} = response}) when is_integer(status),
    do: {status, response}

  defp remote_provider_response(%{} = response), do: {200, response}

  defp send_http_response(socket, status, response) do
    body = Jason.encode!(response)

    :gen_tcp.send(socket, [
      "HTTP/1.1 #{status} #{http_reason(status)}\r\n",
      "content-type: application/json\r\n",
      "content-length: #{byte_size(body)}\r\n",
      "connection: close\r\n",
      "\r\n",
      body
    ])
  end

  defp http_reason(status) when status in 200..299, do: "OK"
  defp http_reason(_status), do: "Error"
end
