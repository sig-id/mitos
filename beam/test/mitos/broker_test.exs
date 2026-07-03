defmodule Mitos.BrokerTest do
  @moduledoc """
  05 §5.3: the egress credential broker — default deny, credential attached at
  the edge (the sandbox never sees it), approval parks, revocation closes the
  lease. Every verdict yields Effect fields keyed to the run + opening Decision.
  """
  use ExUnit.Case, async: true

  alias Mitos.{Broker, Vault}

  defp grant(policy, run_id \\ nil) do
    run_id = run_id || "test/broker_" <> Mitos.id()
    {run_id, Broker.grant(run_id, 7, "seller_x", policy)}
  end

  test "no matching policy rule ⇒ default deny with audit fields" do
    {run_id, token} = grant([%{"dest" => "api.allowed.dev", "credential" => nil}])

    assert {:denied, fields} = Broker.request(token, %{dest: "evil.exfil.io", method: "get"})
    assert fields[:type] == :egress_denied
    assert fields[:dest] == "evil.exfil.io"
    assert fields[:run_id] == run_id
    assert fields[:caused_by] == 7
    assert fields[:reason] == "no_matching_policy"
  end

  test "matching rule attaches the real credential at the edge; sandbox never holds it" do
    dest = "api.broker#{:erlang.unique_integer([:positive])}.dev"
    Vault.put("seller_x", dest, "sekrit-bearer", credential_id: "cred_" <> dest)

    test_pid = self()

    Broker.Destinations.mock(dest, fn outbound ->
      send(test_pid, {:outbound_headers, outbound[:headers]})
      %{status: 200, headers: %{}, body: "granted"}
    end)

    {run_id, token} = grant([%{"dest" => dest, "credential" => "cred_" <> dest}])

    # The sandbox-side request carries NO credential.
    assert {:allowed, resp, fields} =
             Broker.request(token, %{dest: dest, method: "post", headers: %{}, body: "{}"})

    assert resp.status == 200
    assert resp.body == "granted"

    # The credential was attached at the edge — visible only on the outbound leg.
    assert_receive {:outbound_headers, headers}
    assert headers["authorization"] == "Bearer sekrit-bearer"

    # The Effect fields audit the crossing by credential *id*, never material.
    assert fields[:type] == :egress_allowed
    assert fields[:credential_id] == "cred_" <> dest
    assert fields[:run_id] == run_id
    refute inspect(fields) =~ "sekrit"
  end

  test "requires_approval parks the crossing (05 §5.3)" do
    dest = "api.approval#{:erlang.unique_integer([:positive])}.dev"

    {_run_id, token} =
      grant([%{"dest" => dest, "credential" => "c1", "requires_approval" => true}])

    assert {:parked, fields} = Broker.request(token, %{dest: dest, method: "get"})
    assert fields[:type] == :approval_requested
  end

  test "wildcard destination patterns match subdomains" do
    dest = "eu.api.wild.dev"
    Broker.Destinations.mock(dest, fn _ -> %{status: 200, headers: %{}, body: "ok"} end)
    {_run_id, token} = grant([%{"dest" => "*.wild.dev", "credential" => nil}])

    assert {:allowed, _resp, _fields} = Broker.request(token, %{dest: dest, method: "get"})
  end

  test "a revoked lease denies everything (08 §3.2 lease teardown)" do
    {_run_id, token} = grant([%{"dest" => "api.any.dev", "credential" => nil}])
    :ok = Broker.revoke(token)

    assert {:denied, fields} = Broker.request(token, %{dest: "api.any.dev", method: "get"})
    assert fields[:reason] == "no_lease"
  end
end
