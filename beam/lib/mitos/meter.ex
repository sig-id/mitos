defmodule Mitos.Meter do
  @moduledoc """
  The meter — a deterministic fold over the event log (08 §4). Gentix adds NO
  parallel meter: every billable signal is an Effect the substrate already
  emitted, and a charge with no backing event is invalid by construction
  (08 §4.2). The same fold feeds the bill, the proof (08 §5), and the seller's
  attribution line (08 §6) — one source, no drift.
  """

  alias Mitos.Event.Effect

  @doc """
  Derive the charge for a run against an offer (08 §4.2). `offer` carries
  `%{price_usd, payout_addr, seller}` — flat pricing only in the MVP (08 §9).
  """
  def charge(run_id, offer) do
    effects = run_id |> Mitos.Log.read() |> Enum.filter(&match?(%Effect{}, &1))

    compute_ms = compute_ms(effects)
    provider_cost = sum_payload(effects, :budget_charged, "usd")
    tokens = sum_payload(effects, :budget_charged, "tokens")
    egress_units = count(effects, :egress_allowed)
    tool_units = count(effects, :tool_brokered)
    artifacts = count(effects, :artifact_registered)
    delivered? = delivered?(effects)

    buyer_charge = offer[:price_usd] || offer["price_usd"] || 0.0
    compute_cost = compute_ms * config(:compute_rate_usd_per_ms, 0.0)
    compute_surcharge = config(:compute_surcharge_usd, 0.0)

    platform_take =
      if delivered? do
        max(config(:take_floor_usd, 0.05), config(:take_rate, 0.12) * buyer_charge)
      else
        # 08 §7: no platform_take on a non-delivery; the log decides delivery.
        0.0
      end

    %{
      run_id: run_id,
      delivered?: delivered?,
      compute_ms: compute_ms,
      # on the SELLER's own key (08 §4.2) — transparency, never settled by us
      provider_cost: provider_cost,
      tokens: tokens,
      egress_units: egress_units,
      tool_units: tool_units,
      artifacts: artifacts,
      buyer_charge: buyer_charge,
      compute_cost: compute_cost,
      compute_surcharge: compute_surcharge,
      platform_take: platform_take,
      seller_net:
        if(delivered?, do: buyer_charge - platform_take - compute_surcharge, else: 0.0),
      # abort ⇒ escrow refunds the buyer minus compute_surcharge only (08 §7)
      buyer_refund: if(delivered?, do: 0.0, else: max(buyer_charge - compute_surcharge, 0.0))
    }
  end

  # compute clock: capability_started starts it, capability_finished stops it
  # (08 §4.1); across stages the run's clock is first start → last finish.
  defp compute_ms(effects) do
    started = effects |> Enum.filter(&(&1.type == :capability_started)) |> Enum.map(& &1.ts)
    finished = effects |> Enum.filter(&(&1.type == :capability_finished)) |> Enum.map(& &1.ts)

    with [t0 | _] <- Enum.sort(started),
         [t1 | _] <- finished |> Enum.sort() |> Enum.reverse(),
         {:ok, dt0, _} <- DateTime.from_iso8601(t0),
         {:ok, dt1, _} <- DateTime.from_iso8601(t1) do
      max(DateTime.diff(dt1, dt0, :millisecond), 0)
    else
      _ -> 0
    end
  end

  # Delivery = the run finished with its RESULT declared (08 §7): a
  # run_finished{status: finished} whose stages harvested a result.
  defp delivered?(effects) do
    Enum.any?(effects, fn e ->
      e.type == :run_finished and e.payload["status"] == "finished"
    end) and Enum.any?(effects, &(&1.type == :capability_finished))
  end

  defp sum_payload(effects, type, key) do
    effects
    |> Enum.filter(&(&1.type == type))
    |> Enum.reduce(0, fn e, acc -> acc + (e.payload[key] || 0) end)
  end

  defp count(effects, type), do: Enum.count(effects, &(&1.type == type))

  defp config(key, default), do: Application.get_env(:mitos, key, default)
end
