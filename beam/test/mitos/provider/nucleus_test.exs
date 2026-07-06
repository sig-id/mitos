defmodule Mitos.Provider.NucleusTest do
  @moduledoc """
  The Nucleus gVisor provider (05 §3): argv construction + tier eligibility. We
  don't invoke `nucleus` here (it must be built and on PATH; the binary isn't in
  CI) — instead we pin the launch line the provider would run and the launch
  policy's tier reasoning.
  """
  use ExUnit.Case, async: true

  alias Mitos.{LaunchPolicy, Provider}

  defp handle do
    dir = Mitos.Fixtures.capability("nuc-cap")
    {:ok, manifest} = Mitos.Capability.Manifest.load(dir)

    {:ok, spec} =
      LaunchPolicy.compile(manifest, %{run_id: "org/r", stage_id: "s", workspace: nil, trust_tier: "T1"})

    %{spec: spec, workspace: "/ws"}
  end

  test "build_argv wraps the inner harness in a gVisor rootless nucleus run" do
    h = handle()
    argv = Provider.Nucleus.build_argv(h, "claude", ["-p", "hi", "--output-format", "stream-json"])
    adjacent = Enum.zip(argv, tl(argv))

    assert {"--runtime", "gvisor"} in adjacent
    assert {"--workspace", "/ws"} in adjacent
    assert {"--home", "/home/agent"} in adjacent
    assert "--rootless" in argv
    assert "--userns" in argv

    # the inner command follows the `--` separator
    {pre, ["--" | inner]} = Enum.split_while(argv, &(&1 != "--"))
    assert pre != []
    assert hd(inner) == "claude"
    assert "--output-format" in inner
    assert "-p" in inner
  end

  test "build_argv adds --credential-broker pointing at the mitos edge proxy when configured" do
    h = handle()

    # without config: no broker flag
    argv = Provider.Nucleus.build_argv(h, "claude", [])
    refute "--credential-broker" in argv

    # with config: Nucleus enforces broker-only egress to the mitos proxy (05 R1)
    Application.put_env(:mitos, :nucleus_credential_broker, "10.0.42.1:4648")
    argv = Provider.Nucleus.build_argv(h, "claude", [])
    adjacent = Enum.zip(argv, tl(argv))
    assert {"--credential-broker", "10.0.42.1:4648"} in adjacent
    Application.delete_env(:mitos, :nucleus_credential_broker)
  end

  test "build_argv uses --bundle (harness rootfs) when configured, else agent-mode --workspace" do
    h = handle()

    argv = Provider.Nucleus.build_argv(h, "pi", ["-p", "hi"])
    refute "--bundle" in argv
    assert "--workspace" in argv

    Application.put_env(:mitos, :nucleus_bundle, "/tmp/mitos-harness-bundle")
    argv = Provider.Nucleus.build_argv(h, "pi", ["-p", "hi"])
    adjacent = Enum.zip(argv, tl(argv))
    assert {"--bundle", "/tmp/mitos-harness-bundle"} in adjacent
    refute "--workspace" in argv
    Application.delete_env(:mitos, :nucleus_bundle)
  end

  test "capabilities declare gVisor isolation, broker egress, not remote" do
    caps = Provider.Nucleus.capabilities()
    assert caps.isolation == :gvisor
    assert caps.egress == :broker
    assert caps.remote? == false
  end

  test "launch policy admits Nucleus for T1 but T2 still fails closed (05 §4)" do
    # Nucleus is local gVisor ⇒ T1-eligible; T2 requires a remote microVM.
    providers = Application.fetch_env!(:mitos, :providers)
    assert Provider.Nucleus in providers

    caps = Provider.Nucleus.capabilities()
    # T2 needs remote? + microvm isolation; Nucleus has neither
    refute caps.remote?
    refute caps.isolation in [:microvm, :firecracker]

    # sanity: at least one T1 provider is registered (Local or Nucleus)
    t1_eligible = Enum.filter(providers, &(&1.capabilities().egress == :broker))
    assert Provider.Local in t1_eligible
    assert Provider.Nucleus in t1_eligible
  end
end
