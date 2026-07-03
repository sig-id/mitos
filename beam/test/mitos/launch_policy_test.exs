defmodule Mitos.LaunchPolicyTest do
  @moduledoc """
  05 §4: the Tier-1 launch policy compiler maps trust tier → eligible provider
  set + posture, and MUST fail a launch with no eligible provider rather than
  silently downgrade isolation.
  """
  use ExUnit.Case, async: true

  alias Mitos.{Capability, Fixtures, LaunchPolicy}

  defp ctx(over \\ %{}) do
    Map.merge(
      %{run_id: "test/lp_" <> Mitos.id(), stage_id: "s1", workspace: nil, scope: nil},
      over
    )
  end

  test "T1 compiles onto the local provider with an injected harness" do
    manifest = Capability.Manifest.load!(Fixtures.capability("t1cap"))
    assert {:ok, spec} = LaunchPolicy.compile(manifest, ctx())

    assert spec.provider == Mitos.Provider.Local
    assert spec.trust_tier == "T1"
    assert spec.harness == "fake"
    assert spec.harness_mod == Mitos.Harness.Fake
    assert spec.posture.net_mode == :deny_all_except_broker
    # no secret material anywhere in the spec (05 §2)
    refute inspect(spec) =~ "secret"
  end

  test "T2 fails closed when only local providers are registered (never downgrades)" do
    manifest = Capability.Manifest.load!(Fixtures.capability("t2cap", trust_tier: "T2"))
    assert {:error, {:no_eligible_provider, "T2"}} = LaunchPolicy.compile(manifest, ctx())
  end

  test "run context can force a tier upward (facilitator forces T2 at publish, 08 §8)" do
    manifest = Capability.Manifest.load!(Fixtures.capability("t1cap2"))

    assert {:error, {:no_eligible_provider, "T2"}} =
             LaunchPolicy.compile(manifest, ctx(%{trust_tier: "T2"}))
  end

  test "an incompatible harness set is refused, not degraded (03 §5)" do
    manifest =
      Capability.Manifest.load!(Fixtures.capability("nharness", harnesses: ["opencode-v9"]))

    assert {:error, {:no_compatible_harness, ["opencode-v9"]}} =
             LaunchPolicy.compile(manifest, ctx())
  end
end
