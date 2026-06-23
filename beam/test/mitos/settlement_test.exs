defmodule Mitos.SettlementTest do
  use ExUnit.Case, async: true

  test "settlement requires an explicit take floor" do
    workload = %Mitos.Workload{
      id: "paid",
      provider: "fake",
      template: "{{objective}}",
      pricing: %{"type" => "flat", "amount_usdc" => "2.50"},
      payout_address: "usdc:seller:demo"
    }

    receipt = %{
      "quote_id" => "quote-demo",
      "settlement_tx" => "usdc-tx-demo",
      "pay_to" => "usdc:escrow:dev"
    }

    proof = %{
      "effect_log_sha256" => "sha256:proof",
      "delivery" => %{"result" => %{"signal" => "ship"}}
    }

    assert_raise ArgumentError, ~r/take_floor_usdc is required/, fn ->
      Mitos.Settlement.settle(workload, receipt, proof)
    end
  end

  test "verifies x402 payment and records the canonical budget charge idempotently" do
    base_dir = unique_tmp_dir("settlement")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    {:ok, workload} =
      Mitos.Workload.from_json("paid", %{
        "provider" => "fake",
        "template" => "{{objective}}",
        "pricing" => %{
          "type" => "flat",
          "amount_usdc" => "2.50",
          "take_rate_bps" => 1500,
          "take_floor_usdc" => "0.01",
          "payout_address" => "usdc:seller:demo"
        },
        "capability" => %{
          "name" => "paid-demo",
          "harnesses" => ["fake"],
          "trust_tier" => "T1",
          "result" => %{"path" => "RESULT"}
        }
      })

    request = %{"task" => %{"objective" => "paid job"}}
    quote = Mitos.Settlement.payment_quote(workload, request)

    payment =
      Jason.encode!(%{
        quote_id: quote["quote_id"],
        amount_usdc: "2.50",
        currency: "USDC",
        pay_to: quote["pay_to"],
        status: "settled",
        settlement_tx: "usdc-tx-demo"
      })

    assert {:ok, receipt} = Mitos.Settlement.admit(workload, request, payment)
    assert receipt["verified"]

    trace =
      Mitos.Trace.open!(
        run_id: "run-paid",
        base_dir: base_dir,
        launch_spec: %{"provider" => "fake", "task" => "paid job"}
      )

    trace =
      Mitos.Trace.append_decision(trace, %{
        actor: "orchestrator",
        type: "advance",
        target: "cap:test",
        params: %{},
        inputs_hash: "sha256:input",
        rationale: "launch paid capability",
        budget: %{"tokens" => 0, "usd" => 2.5, "wall_ms" => 30_000, "remaining" => %{}}
      })

    trace =
      Mitos.Trace.append_effect(trace, %{
        type: "capability_started",
        caused_by: 0,
        ref: %{},
        payload: %{}
      })

    _trace =
      Mitos.Trace.append_effect(trace, %{
        type: "capability_finished",
        caused_by: 0,
        ref: %{"result" => "RESULT"},
        payload: %{
          "status" => "completed",
          "result" => %{"signal" => "ship"},
          "harvest" => %{"result" => %{"signal" => "ship"}, "artifacts" => []}
        }
      })

    :ok = Mitos.Settlement.record_charge!("run-paid", workload, receipt, base_dir: base_dir)
    :ok = Mitos.Settlement.record_charge!("run-paid", workload, receipt, base_dir: base_dir)

    proof = Mitos.Proof.build("run-paid", base_dir: base_dir)
    settlement = Mitos.Settlement.settle(workload, receipt, proof)

    assert proof["metering"]["budget_charged_units"] == 1
    assert proof["metering"]["budget_charged"]["usd"] == 2.5

    assert [
             %{
               "quote_id" => quote_id,
               "settlement_tx" => "usdc-tx-demo",
               "status" => "settled",
               "buyer_charge_usdc" => "2.500000",
               "refund_due_usdc" => "0.000000",
               "platform_take_usdc" => "0.375000",
               "seller_net_usdc" => "2.125000"
             }
           ] = proof["settlements"]

    assert quote_id == receipt["quote_id"]

    assert settlement["escrowed_usdc"] == "2.500000"
    assert settlement["buyer_charge_usdc"] == "2.500000"
    assert settlement["refund_due_usdc"] == "0.000000"
    assert settlement["platform_take_usdc"] == "0.375000"
    assert settlement["seller_net_usdc"] == "2.125000"
    assert settlement["proof_hash"] == proof["effect_log_sha256"]
  end

  test "refund-due proof yields no platform take or seller payout" do
    {:ok, workload} =
      Mitos.Workload.from_json("paid-refund", %{
        "provider" => "fake",
        "template" => "{{objective}}",
        "pricing" => %{
          "type" => "flat",
          "amount_usdc" => "2.50",
          "take_rate_bps" => 1500,
          "compute_surcharge_usdc" => "0.15",
          "take_floor_usdc" => "0.01",
          "payout_address" => "usdc:seller:demo"
        },
        "capability" => %{
          "name" => "paid-demo",
          "harnesses" => ["fake"],
          "trust_tier" => "T1",
          "result" => %{"path" => "RESULT"}
        }
      })

    receipt = %{
      "quote_id" => "quote_refund",
      "settlement_tx" => "usdc-tx-refund",
      "pay_to" => "usdc:escrow:dev"
    }

    proof = %{
      "effect_log_sha256" => "sha256:proof",
      "delivery" => %{"result" => %{"synthetic" => true}}
    }

    settlement = Mitos.Settlement.settle(workload, receipt, proof)

    assert settlement["status"] == "refund_due"
    assert settlement["escrowed_usdc"] == "2.500000"
    assert settlement["buyer_charge_usdc"] == "0.150000"
    assert settlement["refund_due_usdc"] == "2.350000"
    assert settlement["compute_surcharge_usdc"] == "0.150000"
    assert settlement["platform_take_usdc"] == "0.000000"
    assert settlement["seller_net_usdc"] == "0.000000"
    assert settlement["proof_hash"] == "sha256:proof"
  end

  test "refund-due paid run records only compute surcharge in the billing ledger" do
    base_dir = unique_tmp_dir("settlement-refund-ledger")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    {:ok, workload} =
      Mitos.Workload.from_json("paid-refund-ledger", %{
        "provider" => "fake",
        "template" => "{{objective}}",
        "pricing" => %{
          "type" => "flat",
          "amount_usdc" => "2.50",
          "take_rate_bps" => 1500,
          "compute_surcharge_usdc" => "0.15",
          "take_floor_usdc" => "0.01",
          "payout_address" => "usdc:seller:demo"
        },
        "capability" => %{
          "name" => "paid-demo",
          "harnesses" => ["fake"],
          "trust_tier" => "T1",
          "result" => %{"path" => "RESULT"}
        }
      })

    request = %{"task" => %{"objective" => "paid job"}}
    quote = Mitos.Settlement.payment_quote(workload, request)

    payment =
      Jason.encode!(%{
        quote_id: quote["quote_id"],
        amount_usdc: "2.50",
        currency: "USDC",
        pay_to: quote["pay_to"],
        status: "settled",
        settlement_tx: "usdc-tx-refund-ledger"
      })

    assert {:ok, receipt} = Mitos.Settlement.admit(workload, request, payment)

    trace =
      Mitos.Trace.open!(
        run_id: "run-refund-ledger",
        base_dir: base_dir,
        launch_spec: %{"provider" => "fake", "task" => "paid job"}
      )

    trace =
      Mitos.Trace.append_decision(trace, %{
        actor: "orchestrator",
        type: "advance",
        target: "cap:test",
        params: %{},
        inputs_hash: "sha256:input",
        rationale: "launch paid capability",
        budget: %{"tokens" => 0, "usd" => 2.5, "wall_ms" => 30_000, "remaining" => %{}}
      })

    _trace =
      Mitos.Trace.append_effect(trace, %{
        type: "capability_finished",
        caused_by: 0,
        ref: %{"result" => "RESULT"},
        payload: %{
          "status" => "failed",
          "result" => %{"synthetic" => true, "needs" => ["result_missing"]},
          "harvest" => %{
            "result" => %{"synthetic" => true, "needs" => ["result_missing"]},
            "artifacts" => []
          }
        }
      })

    :ok =
      Mitos.Settlement.record_charge!("run-refund-ledger", workload, receipt, base_dir: base_dir)

    proof = Mitos.Proof.build("run-refund-ledger", base_dir: base_dir)
    settlement = Mitos.Settlement.settle(workload, receipt, proof)
    quote_id = quote["quote_id"]

    assert settlement["status"] == "refund_due"
    assert settlement["buyer_charge_usdc"] == "0.150000"
    assert settlement["platform_take_usdc"] == "0.000000"
    assert settlement["seller_net_usdc"] == "0.000000"
    assert proof["metering"]["budget_charged"]["usd"] == 0.15

    assert Enum.any?(Mitos.Trace.read_events("run-refund-ledger", base_dir: base_dir), fn
             %{
               "kind" => "effect",
               "type" => "budget_charged",
               "ref" => %{"quote_id" => ^quote_id},
               "payload" => %{
                 "billing_basis" => "flat_price",
                 "amount_usdc" => "0.150000",
                 "usd_micros" => 150_000
               }
             } ->
               true

             _event ->
               false
           end)
  end

  test "metered pricing charges log-derived units under the escrow ceiling" do
    base_dir = unique_tmp_dir("settlement-metered")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    {:ok, workload} =
      Mitos.Workload.from_json("paid-metered", %{
        "provider" => "fake",
        "template" => "{{objective}}",
        "pricing" => %{
          "type" => "metered",
          "amount_usdc" => "5.00",
          "base_usdc" => "0.25",
          "unit_prices_usdc" => %{
            "token" => "0.01",
            "egress" => "0.10",
            "tool" => "0.05",
            "artifact" => "0.20"
          },
          "take_rate_bps" => 1500,
          "take_floor_usdc" => "0.01",
          "payout_address" => "usdc:seller:demo"
        },
        "capability" => %{
          "name" => "paid-demo",
          "harnesses" => ["fake"],
          "trust_tier" => "T1",
          "result" => %{"path" => "RESULT"}
        }
      })

    request = %{"task" => %{"objective" => "metered job"}}
    quote = Mitos.Settlement.payment_quote(workload, request)
    assert quote["amount_usdc"] == "5.00"

    payment =
      Jason.encode!(%{
        quote_id: quote["quote_id"],
        amount_usdc: "5.00",
        currency: "USDC",
        pay_to: quote["pay_to"],
        status: "settled",
        settlement_tx: "usdc-tx-metered"
      })

    assert {:ok, receipt} = Mitos.Settlement.admit(workload, request, payment)

    trace =
      Mitos.Trace.open!(
        run_id: "run-metered",
        base_dir: base_dir,
        launch_spec: %{"provider" => "fake", "task" => "metered job"}
      )

    trace =
      Mitos.Trace.append_decision(trace, %{
        actor: "orchestrator",
        type: "advance",
        target: "cap:test",
        params: %{},
        inputs_hash: "sha256:input",
        rationale: "launch metered capability",
        budget: %{"tokens" => 0, "usd" => 5.0, "wall_ms" => 30_000, "remaining" => %{}}
      })

    trace =
      Mitos.Trace.append_effect(trace, %{
        type: "capability_started",
        caused_by: 0,
        ref: %{},
        payload: %{}
      })

    trace =
      Mitos.Trace.append_effect(trace, %{
        type: "budget_charged",
        caused_by: 0,
        ref: %{"provider" => "fake", "model" => "gpt-test"},
        payload: %{
          "billing_basis" => "provider_token_usage",
          "tokens" => 15,
          "usd" => 0.25,
          "wall_ms" => 500
        }
      })

    trace =
      Mitos.Trace.append_effect(trace, %{
        type: "egress_allowed",
        caused_by: 0,
        ref: %{"dest" => "api.example.test"},
        payload: %{}
      })

    trace =
      Mitos.Trace.append_effect(trace, %{
        type: "tool_brokered",
        caused_by: 0,
        ref: %{"tool" => "fetch"},
        payload: %{}
      })

    trace =
      Mitos.Trace.append_effect(trace, %{
        type: "tool_brokered",
        caused_by: 0,
        ref: %{"tool" => "parse"},
        payload: %{}
      })

    trace =
      Mitos.Trace.append_effect(trace, %{
        type: "artifact_registered",
        caused_by: 0,
        ref: %{"kind" => "file", "path" => "report.md", "sha256" => "sha256:report"},
        payload: %{}
      })

    _trace =
      Mitos.Trace.append_effect(trace, %{
        type: "capability_finished",
        caused_by: 0,
        ref: %{"result" => "RESULT"},
        payload: %{
          "status" => "completed",
          "result" => %{"signal" => "ship"},
          "harvest" => %{"result" => %{"signal" => "ship"}, "artifacts" => []}
        }
      })

    :ok = Mitos.Settlement.record_charge!("run-metered", workload, receipt, base_dir: base_dir)

    proof = Mitos.Proof.build("run-metered", base_dir: base_dir)
    settlement = Mitos.Settlement.settle(workload, receipt, proof)
    quote_id = quote["quote_id"]

    assert proof["metering"]["budget_charged_units"] == 2

    assert Enum.any?(Mitos.Trace.read_events("run-metered", base_dir: base_dir), fn
             %{
               "kind" => "effect",
               "type" => "budget_charged",
               "ref" => %{"quote_id" => ^quote_id, "pricing_type" => "metered"},
               "payload" => %{"billing_basis" => "metered_price", "amount_usdc" => "0.800000"}
             } ->
               true

             _ ->
               false
           end)

    assert settlement["escrowed_usdc"] == "5.000000"
    assert settlement["buyer_charge_usdc"] == "0.800000"
    assert settlement["refund_due_usdc"] == "4.200000"
    assert settlement["platform_take_usdc"] == "0.120000"
    assert settlement["seller_net_usdc"] == "0.680000"
  end

  test "records settlement charge against its own finish decision budget" do
    base_dir = unique_tmp_dir("settlement-finish-budget")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    {:ok, workload} =
      Mitos.Workload.from_json("paid-over-budget", %{
        "provider" => "fake",
        "template" => "{{objective}}",
        "pricing" => %{
          "type" => "flat",
          "amount_usdc" => "3.00",
          "take_floor_usdc" => "0.01",
          "payout_address" => "usdc:seller:demo"
        },
        "capability" => %{
          "name" => "paid-demo",
          "harnesses" => ["fake"],
          "trust_tier" => "T1",
          "result" => %{"path" => "RESULT"}
        }
      })

    request = %{"task" => %{"objective" => "paid job"}}
    quote = Mitos.Settlement.payment_quote(workload, request)

    payment =
      Jason.encode!(%{
        quote_id: quote["quote_id"],
        amount_usdc: "3.00",
        currency: "USDC",
        pay_to: quote["pay_to"],
        status: "settled",
        settlement_tx: "usdc-tx-over-budget"
      })

    assert {:ok, receipt} = Mitos.Settlement.admit(workload, request, payment)

    trace =
      Mitos.Trace.open!(
        run_id: "run-finish-budget",
        base_dir: base_dir,
        launch_spec: %{"provider" => "fake", "task" => "paid job"}
      )

    trace =
      Mitos.Trace.append_decision(trace, %{
        actor: "orchestrator",
        type: "advance",
        target: "cap:test",
        params: %{},
        inputs_hash: "sha256:input",
        rationale: "launch paid capability",
        budget: %{"tokens" => 0, "usd" => 2.5, "wall_ms" => 30_000, "remaining" => %{}}
      })

    _trace =
      Mitos.Trace.append_effect(trace, %{
        type: "capability_finished",
        caused_by: 0,
        ref: %{"result" => "RESULT"},
        payload: %{
          "status" => "completed",
          "result" => %{"signal" => "ship"},
          "harvest" => %{"result" => %{"signal" => "ship"}, "artifacts" => []}
        }
      })

    :ok =
      Mitos.Settlement.record_charge!("run-finish-budget", workload, receipt, base_dir: base_dir)

    events = Mitos.Trace.read_events("run-finish-budget", base_dir: base_dir)

    finish =
      Enum.find(events, fn
        %{
          "kind" => "decision",
          "type" => "finish",
          "params" => %{"settlement_quote_id" => quote_id}
        } ->
          quote_id == quote["quote_id"]

        _event ->
          false
      end)

    assert %{"seq" => finish_seq, "budget" => %{"usd" => 3.0, "remaining" => %{"usd" => 3.0}}} =
             finish

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "budget_charged",
               "caused_by" => ^finish_seq,
               "ref" => %{"quote_id" => quote_id},
               "payload" => %{"amount_usdc" => "3.000000"}
             } ->
               quote_id == quote["quote_id"]

             _event ->
               false
           end)

    proof = Mitos.Proof.build("run-finish-budget", base_dir: base_dir)
    assert proof["metering"]["budget_charged_units"] == 1
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end
end
