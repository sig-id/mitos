defmodule Mitos.FacilitatorTest do
  @moduledoc """
  The success gate (08 §9), end to end: one stranger-paid, machine-fulfilled
  transaction, with proof. x402 admission → run in the buyer's context with
  the seller's key brokered → harvest → deliverable + proof page → metered
  split settled. Plus the fail-closed path: a T2 marketplace with no remote
  provider refuses the launch and refunds the buyer.
  """
  use ExUnit.Case, async: false
  import Plug.Test
  import Plug.Conn

  alias Mitos.{Fixtures, Run, Vault}
  alias Mitos.Web.Router

  @moduletag timeout: 120_000
  @opts Router.init([])

  defp post_json(path, body, headers \\ []) do
    conn =
      conn(:post, path, Mitos.JSON.encode!(body))
      |> put_req_header("content-type", "application/json")

    conn = Enum.reduce(headers, conn, fn {k, v}, c -> put_req_header(c, k, v) end)
    Router.call(conn, @opts)
  end

  defp get_json(path) do
    conn = Router.call(conn(:get, path), @opts)
    {conn.status, Mitos.JSON.decode!(conn.resp_body)}
  end

  defp publish_seller_capability do
    dest = "api.seller#{:erlang.unique_integer([:positive])}.example"
    cred_id = "seller_key_" <> dest
    Vault.put("designer_org", dest, "her-provider-key", credential_id: cred_id)

    Mitos.Broker.Destinations.mock(dest, fn outbound ->
      if outbound[:headers]["authorization"] == "Bearer her-provider-key" do
        %{status: 200, headers: %{}, body: ~s({"design":"three directions"})}
      else
        %{status: 401, headers: %{}, body: "unauthenticated"}
      end
    end)

    script = """
    #!/bin/sh
    set -e
    curl -s -X POST "$MITOS_BROKER/egress" \
      -H "x-mitos-egress-token: $MITOS_EGRESS_TOKEN" \
      -H "content-type: application/json" \
      -d '{"dest":"#{dest}","method":"post","body":"{}"}' > provider_response.json
    printf '# Design critique\\n\\nDirection A wins.\\n' > critique.md
    cat > "$MITOS_RESULT" <<'EOF'
    {"signal":"ship","confidence":0.95,"summary":"critique delivered",
     "metrics":{"directions":3},
     "evidence":[{"kind":"report","path":"critique.md"}]}
    EOF
    echo '{"tokens": 800, "usd": 0.4}' > "$MITOS_USAGE"
    """

    cap_dir =
      Fixtures.capability("designcritique",
        script: script,
        egress_policy: [%{"dest" => dest, "credential" => cred_id}],
        result: %{"evidence" => ["critique.md", "provider_response.json"]}
      )

    {:ok, capability_id} =
      Mitos.Facilitator.publish(cap_dir, %{
        price_usd: 5.0,
        payout_addr: "0xSELLER_PAYOUT",
        seller: "designer_org"
      })

    capability_id
  end

  test "the gate: stranger pays via x402, machine fulfills, proof + split settle" do
    capability_id = publish_seller_capability()

    # 1 — no payment ⇒ 402 Payment Required with the quote (08 §7).
    conn = post_json("/capabilities/#{capability_id}/runs", %{inputs: %{"brief" => "landing page"}})
    assert conn.status == 402
    quote_body = Mitos.JSON.decode!(conn.resp_body)
    assert quote_body["amount_usdc"] == 5.0
    quote_id = quote_body["quote_id"]

    # 2 — repeat with X-PAYMENT ⇒ admitted, run starts.
    payment =
      Mitos.JSON.encode!(%{quote_id: quote_id, amount_usdc: 5.0, payer: "0xSTRANGER"})

    conn =
      post_json("/capabilities/#{capability_id}/runs", %{inputs: %{"brief" => "landing page"}},
        [{"x-payment", payment}]
      )

    assert conn.status == 202
    %{"run_id" => run_id, "task_id" => task_id} = Mitos.JSON.decode!(conn.resp_body)
    assert run_id =~ ~r{\Abuyer_0xSTRANGER/}

    # 3 — the machine fulfills.
    state = Run.await(run_id)
    assert state.status == :finished
    assert Mitos.WorkPool.get(task_id).status == :done

    # 4 — delivery: artifacts + RESULT, pulled from the log's read side.
    {200, deliverable} = get_json("/runs/#{run_id}/deliverable")
    assert deliverable["result"]["signal"] == "ship"
    assert Enum.any?(deliverable["artifacts"], &(&1["path"] == "critique.md"))
    assert deliverable["diff"] =~ "critique.md"

    # 5 — the proof page: what happened, never the recipe (08 §5).
    {200, proof} = get_json("/runs/#{run_id}/proof")
    assert proof["status"] == "finished"
    assert proof["log_hash"] =~ ~r/\A[a-f0-9]{64}\z/
    assert proof["token_totals"]["tokens"] == 800
    assert [%{"outcome" => "egress_allowed"} | _] = proof["egress"]
    assert proof["charge"]["buyer_charge_usd"] == 5.0
    assert proof["charge"]["delivered"] == true

    raw_proof = Mitos.JSON.encode!(proof)
    # Redactions: no Decision rationale, no harness identity, no skill text,
    # no key material (01 §8.1, 08 §5).
    refute raw_proof =~ "rationale"
    refute raw_proof =~ "harness"
    refute raw_proof =~ "skill"
    refute raw_proof =~ "her-provider-key"

    # 6 — settlement: fold → split → transfer (08 §4.2/§7).
    settlement = Mitos.Facilitator.settlement(run_id)
    assert settlement.charge.delivered?
    assert_in_delta settlement.charge.platform_take, 0.6, 1.0e-9
    assert_in_delta settlement.charge.seller_net, 4.4, 1.0e-9
    # provider tokens are the seller's own-key cost — visible, never settled
    assert_in_delta settlement.charge.provider_cost, 0.4, 1.0e-9

    kinds = Enum.map(settlement.transfers, & &1.kind)
    assert :seller_payout in kinds
    assert :platform_take in kinds
    refute :buyer_refund in kinds

    payout = Enum.find(Mitos.Settlement.Mock.transfers(), &(&1.memo[:run_id] == run_id and &1.to == "0xSELLER_PAYOUT"))
    assert_in_delta payout.amount, 4.4, 1.0e-9

    # 7 — the run status page shows the buyer their settlement line.
    {200, status} = get_json("/runs/#{run_id}")
    assert status["settlement"]["delivered"] == true
  end

  test "T2 marketplace with no remote provider fails closed and refunds (05 §4, 08 §7)" do
    prev = Application.get_env(:mitos, :marketplace_tier)
    Application.put_env(:mitos, :marketplace_tier, "T2")
    on_exit(fn -> Application.put_env(:mitos, :marketplace_tier, prev) end)

    cap_dir = Fixtures.capability("t2only")

    {:ok, capability_id} =
      Mitos.Facilitator.publish(cap_dir, %{
        price_usd: 3.0,
        payout_addr: "0xSELLER2",
        seller: "seller2"
      })

    conn = post_json("/capabilities/#{capability_id}/runs", %{inputs: %{}})
    quote_id = Mitos.JSON.decode!(conn.resp_body)["quote_id"]

    payment = Mitos.JSON.encode!(%{quote_id: quote_id, amount_usdc: 3.0, payer: "0xREFUNDME"})

    conn = post_json("/capabilities/#{capability_id}/runs", %{inputs: %{}}, [{"x-payment", payment}])
    assert conn.status == 202
    %{"run_id" => run_id} = Mitos.JSON.decode!(conn.resp_body)

    # Isolation is never silently downgraded: the launch is refused, the run aborts.
    state = Run.await(run_id)
    assert state.status == :aborted

    settlement = Mitos.Facilitator.settlement(run_id)
    refute settlement.charge.delivered?
    # no platform take on a non-delivery; buyer refunded (minus surcharge = 0)
    assert settlement.charge.platform_take == 0.0
    assert_in_delta settlement.charge.buyer_refund, 3.0, 1.0e-9
    assert Enum.any?(settlement.transfers, &(&1.kind == :buyer_refund))
  end

  test "underpayment is refused admission — nothing billable is emitted (08 §7)" do
    capability_id = publish_seller_capability()

    conn = post_json("/capabilities/#{capability_id}/runs", %{inputs: %{}})
    quote_id = Mitos.JSON.decode!(conn.resp_body)["quote_id"]

    payment = Mitos.JSON.encode!(%{quote_id: quote_id, amount_usdc: 0.5, payer: "0xCHEAP"})
    conn = post_json("/capabilities/#{capability_id}/runs", %{inputs: %{}}, [{"x-payment", payment}])

    assert conn.status == 402
    assert Mitos.JSON.decode!(conn.resp_body)["error"] == "payment_rejected"
  end
end
