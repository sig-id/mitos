defmodule Mitos.MeterTest do
  @moduledoc "08 §4.2: the charge is a deterministic fold over the event log."
  use ExUnit.Case, async: true

  alias Mitos.{Log, Meter}
  alias Mitos.Event.Effect

  defp synthetic_run(status) do
    run_id = "test/meter_" <> Mitos.id()
    {:ok, w, []} = Log.open_writer(run_id)

    t0 = ~U[2026-07-03 10:00:00.000Z]

    events = [
      Effect.new(type: :run_started, ts: iso(t0, 0)),
      Effect.new(type: :capability_started, ts: iso(t0, 100), caused_by: 0),
      Effect.new(type: :egress_allowed, ts: iso(t0, 500), caused_by: 0),
      Effect.new(type: :egress_denied, ts: iso(t0, 600), caused_by: 0),
      Effect.new(type: :artifact_registered, ts: iso(t0, 900), caused_by: 0),
      Effect.new(
        type: :budget_charged,
        ts: iso(t0, 1900),
        caused_by: 0,
        payload: %{"tokens" => 500, "usd" => 0.25, "wall_ms" => 1800}
      ),
      Effect.new(type: :capability_finished, ts: iso(t0, 2100), caused_by: 0),
      Effect.new(type: :run_finished, ts: iso(t0, 2200), payload: %{"status" => status})
    ]

    Enum.reduce(events, w, fn e, w -> Log.append(w, e) |> elem(1) end) |> Log.close()
    run_id
  end

  defp iso(t0, ms), do: t0 |> DateTime.add(ms, :millisecond) |> DateTime.to_iso8601()

  test "delivered run: take-rate split with floor, provider cost pass-through" do
    charge = Meter.charge(synthetic_run("finished"), %{price_usd: 10.0})

    assert charge.delivered?
    assert charge.compute_ms == 2000
    assert charge.tokens == 500
    assert_in_delta charge.provider_cost, 0.25, 1.0e-9
    assert charge.egress_units == 1
    assert charge.artifacts == 1
    assert charge.buyer_charge == 10.0
    # take_rate 0.12 × 10 = 1.2 > floor 0.05
    assert_in_delta charge.platform_take, 1.2, 1.0e-9
    assert_in_delta charge.seller_net, 8.8, 1.0e-9
    assert charge.buyer_refund == 0.0
  end

  test "the take floor keeps cheap jobs from running net-negative" do
    charge = Meter.charge(synthetic_run("finished"), %{price_usd: 0.10})
    assert_in_delta charge.platform_take, 0.05, 1.0e-9
  end

  test "aborted run: no take, buyer refunded (08 §7 failure refunds)" do
    charge = Meter.charge(synthetic_run("aborted"), %{price_usd: 10.0})

    refute charge.delivered?
    assert charge.platform_take == 0.0
    assert charge.seller_net == 0.0
    assert_in_delta charge.buyer_refund, 10.0, 1.0e-9
  end
end
