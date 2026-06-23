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
    assert {:error, :use_session_supervisor} = provider.start(%{}, [], %{})
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
end
