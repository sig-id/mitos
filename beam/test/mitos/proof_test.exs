defmodule Mitos.ProofTest do
  use ExUnit.Case, async: true

  test "folds buyer proof from effects without leaking decisions or harness recipe details" do
    events = [
      %{
        "kind" => "decision",
        "run_id" => "run-proof",
        "seq" => 0,
        "ts" => "2026-06-22T10:00:00.000Z",
        "actor" => "orchestrator",
        "type" => "advance",
        "target" => "cap:test",
        "params" => %{"prompt" => "seller recipe detail"},
        "inputs_hash" => "sha256:input",
        "rationale" => "secret rationale text",
        "budget" => %{}
      },
      %{
        "kind" => "effect",
        "run_id" => "run-proof",
        "seq" => 1,
        "ts" => "2026-06-22T10:00:01.000Z",
        "type" => "capability_started",
        "caused_by" => 0,
        "ref" => %{"provider" => "fake", "adapter" => "secret-harness"},
        "payload" => %{"harness" => "secret-harness"}
      },
      %{
        "kind" => "effect",
        "run_id" => "run-proof",
        "seq" => 2,
        "ts" => "2026-06-22T10:00:02.000Z",
        "type" => "tool_brokered",
        "caused_by" => 0,
        "ref" => %{"tool" => "fetch_url", "schema" => "tool.fetch.v1"},
        "payload" => %{}
      },
      %{
        "kind" => "effect",
        "run_id" => "run-proof",
        "seq" => 3,
        "ts" => "2026-06-22T10:00:03.000Z",
        "type" => "egress_allowed",
        "caused_by" => 0,
        "ref" => %{
          "dest" => "api.example.test",
          "credential_id" => "cred-secret",
          "policy_rule" => "seller-private-policy"
        },
        "payload" => %{}
      },
      %{
        "kind" => "effect",
        "run_id" => "run-proof",
        "seq" => 4,
        "ts" => "2026-06-22T10:00:04.000Z",
        "type" => "budget_charged",
        "caused_by" => 0,
        "ref" => %{},
        "payload" => %{"tokens" => 12, "usd" => 0.25, "wall_ms" => 300}
      },
      %{
        "kind" => "effect",
        "run_id" => "run-proof",
        "seq" => 5,
        "ts" => "2026-06-22T10:00:05.000Z",
        "type" => "artifact_registered",
        "caused_by" => 0,
        "ref" => %{"kind" => "file", "path" => "report.md", "sha256" => "sha256:report"},
        "payload" => %{}
      },
      %{
        "kind" => "effect",
        "run_id" => "run-proof",
        "seq" => 6,
        "ts" => "2026-06-22T10:00:06.000Z",
        "type" => "capability_finished",
        "caused_by" => 0,
        "ref" => %{"result" => "RESULT"},
        "payload" => %{
          "status" => "completed",
          "result" => %{"signal" => "ship", "summary" => "done", "path" => "RESULT"},
          "harvest" => %{
            "diff" => %{"path" => "diff.patch", "sha256" => "sha256:diff"},
            "result" => %{
              "signal" => "ship",
              "path" => "RESULT",
              "sha256" => "sha256:result",
              "evidence" => [
                %{
                  "kind" => "file",
                  "path" => "scores.json",
                  "sha256" => "sha256:scores",
                  "bytes" => 32
                },
                %{"kind" => "file", "path" => "missing.md", "missing" => true}
              ]
            },
            "artifacts" => [
              %{"kind" => "file", "path" => "report.md", "sha256" => "sha256:report"}
            ]
          }
        }
      },
      %{
        "kind" => "effect",
        "run_id" => "run-proof",
        "seq" => 7,
        "ts" => "2026-06-22T10:00:07.000Z",
        "type" => "budget_charged",
        "caused_by" => 0,
        "ref" => %{
          "quote_id" => "quote_demo",
          "resource" => "/capabilities/paid/runs",
          "settlement_tx" => "usdc-tx-demo",
          "pricing_type" => "flat"
        },
        "payload" => %{
          "tokens" => 0,
          "usd" => 2.5,
          "wall_ms" => 0,
          "amount_usdc" => "2.500000",
          "currency" => "USDC",
          "settlement" => %{
            "status" => "settled",
            "quote_id" => "quote_demo",
            "settlement_tx" => "usdc-tx-demo",
            "currency" => "USDC",
            "escrowed_usdc" => "2.500000",
            "buyer_charge_usdc" => "2.500000",
            "refund_due_usdc" => "0.000000",
            "compute_surcharge_usdc" => "0.000000",
            "platform_take_usdc" => "0.375000",
            "seller_net_usdc" => "2.125000",
            "pay_to" => "usdc:escrow:dev"
          }
        }
      }
    ]

    proof = Mitos.Proof.from_events(events)

    assert proof["run_id"] == "run-proof"
    assert proof["effect_log_sha256"] =~ "sha256:"
    assert proof["redaction"]["decision_events_excluded"]
    assert proof["redaction"]["rationale_redacted"]

    assert proof["metering"]["effect_units"] == 7
    assert proof["metering"]["tool_brokered_units"] == 1
    assert proof["metering"]["egress_allowed_units"] == 1
    assert proof["metering"]["artifact_registered_units"] == 1
    assert proof["metering"]["compute_ms"] == 5_000

    assert proof["metering"]["budget_charged"] == %{
             "tokens" => 12,
             "usd" => 2.75,
             "wall_ms" => 300
           }

    assert [%{"tool" => "fetch_url", "schema" => "tool.fetch.v1"}] = proof["tools_brokered"]
    assert [%{"dest" => "api.example.test", "decision" => "allowed"}] = proof["egress_crossings"]

    assert %{"kind" => "file", "path" => "report.md", "sha256" => "sha256:report"} in proof[
             "artifacts"
           ]

    assert %{
             "kind" => "file",
             "path" => "scores.json",
             "sha256" => "sha256:scores",
             "bytes" => 32
           } in proof["artifacts"]

    assert %{"kind" => "file", "path" => "missing.md", "missing" => true} in proof["artifacts"]

    assert proof["delivery"]["result"]["signal"] == "ship"
    assert proof["delivery"]["diff"]["sha256"] == "sha256:diff"

    assert [
             %{
               "quote_id" => "quote_demo",
               "settlement_tx" => "usdc-tx-demo",
               "status" => "settled",
               "buyer_charge_usdc" => "2.500000",
               "refund_due_usdc" => "0.000000",
               "platform_take_usdc" => "0.375000",
               "seller_net_usdc" => "2.125000",
               "pay_to" => "usdc:escrow:dev"
             }
           ] = proof["settlements"]

    html = Mitos.Proof.render_html(proof)
    assert html =~ "RESULT"
    assert html =~ "sha256:result"
    assert html =~ "scores.json"
    assert html =~ "sha256:scores"
    assert html =~ "missing.md"
    assert html =~ "missing"
    assert html =~ "Settlement"
    assert html =~ "usdc-tx-demo"
    assert html =~ "0.375000"

    rendered = inspect(proof)
    refute rendered =~ "secret rationale text"
    refute rendered =~ "seller recipe detail"
    refute rendered =~ "secret-harness"
    refute rendered =~ "cred-secret"
    refute rendered =~ "seller-private-policy"
  end
end
