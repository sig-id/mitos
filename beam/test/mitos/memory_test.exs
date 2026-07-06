defmodule Mitos.MemoryTest do
  @moduledoc """
  07: two-track memory with contradiction-supersession, and verify→distill that
  writes long-term lessons only from a verified run.
  """
  use ExUnit.Case, async: false

  setup do
    on_exit(&Mitos.Memory.clear/0)
    :ok
  end

  test "long-term write at an existing (scope,key) supersedes the prior; ephemeral does not" do
    Mitos.Memory.write("acme", :"profile.scope", "web app, no crypto", track: :long_term)
    b = Mitos.Memory.write("acme", :"profile.scope", "web app + JWT auth", track: :long_term)

    # the prior is superseded (retained for audit, not retrieved); the new one is current
    profile = Mitos.Memory.profile("acme")
    assert length(profile) == 1
    assert hd(profile).id == b.id
    assert hd(profile).content =~ "JWT auth"

    # ephemeral writes coexist (scratch notes, not superseding)
    Mitos.Memory.write("acme", :scratch, "v1", track: :ephemeral)
    Mitos.Memory.write("acme", :scratch, "v2", track: :ephemeral)
    assert Enum.count(Mitos.Memory.profile("acme"), &(&1.track == :ephemeral)) == 2
  end

  test "retrieve returns non-superseded slices ranked to the query" do
    Mitos.Memory.write("acme", :a, "authn uses JWT", track: :long_term)
    Mitos.Memory.write("acme", :b, "deployed on fly.io", track: :long_term)

    slices = Mitos.Memory.retrieve("acme", "auth")
    assert hd(slices) =~ "JWT"
  end

  test "distill writes long-term lessons only from a verified (ship/pass) run" do
    # a finished run with a ship signal
    dir = Mitos.Fixtures.capability("mem-cap")
    {:ok, _manifest} = Mitos.Capability.Manifest.load(dir)

    {:ok, run_id} =
      Mitos.Run.start_run(
        tenant: %{"org" => "acme", "project" => "mem"},
        pipeline: Mitos.Fixtures.pipeline(["mem-cap"], name: "mem"),
        capabilities: %{"mem-cap" => dir},
        inputs: %{}
      )

    Mitos.Run.await(run_id)

    written = Mitos.Memory.distill(run_id, scope: "acme")
    assert is_list(written)
    # the Fake capability ships a 'ship' signal ⇒ lessons were written
    refute written == []
    assert Enum.any?(Mitos.Memory.profile("acme"), &(&1.track == :long_term))
  end
end
